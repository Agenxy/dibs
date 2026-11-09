// SPDX-License-Identifier: GPL-3.0-or-later
// Copyright 2026 Agenxy

import type { Page } from "playwright"
import { mkdtempSync, rmSync } from "node:fs"
import { tmpdir } from "node:os"
import { join } from "node:path"

type Check = (name: string, ok: boolean, detail?: string) => void
type Tool = (name: string, args: Record<string, any>) => Promise<any>

// The real password-unlocked board, real mail and real file inputs. These
// pixels are synthetic fixture artwork; no camera, clipboard or real board.
export async function checkWebMedia(page: Page, check: Check, tool: Tool, token: string, recipient: string) {
  const fixture = await page.evaluate(async () => {
    const canvas = document.createElement("canvas")
    canvas.width = 640; canvas.height = 320
    const context = canvas.getContext("2d")!
    context.fillStyle = "#192d46"; context.fillRect(0, 0, 640, 320)
    context.fillStyle = "#9ac0ec"; context.fillRect(32, 32, 576, 88)
    context.fillStyle = "#192d46"; context.font = "bold 28px sans-serif"
    context.fillText("Concept image", 54, 88)
    for (let i = 0; i < 4; i++) {
      context.fillStyle = i % 2 ? "#ffb982" : "#94b9e8"
      context.fillRect(32 + i * 148, 152, 132, 128)
    }
    return canvas.toDataURL("image/png").split(",")[1]
  })
  const put = async (data: Buffer, mime: string) => {
    const out = await tool("put_blob", {token, data: data.toString("base64"), mime})
    if (!out.blob) throw new Error("setup: media fixture blob was refused")
    return out.blob as string
  }
  const send = async (blob: string, body: string, type = "question") => {
    const out = await tool("send", {token, to: "human", type, body, attachments:[{blob}]})
    if (!out.msg_serial) throw new Error("setup: media question was refused")
    return out.msg_serial as number
  }
  const image = await put(Buffer.from(fixture, "base64"), "image/png")
  const serial = await send(image, "Visual concept for the person")
  const unsafe = await put(Buffer.from("<script>globalThis.mediaExecuted=true</script>"), "text/html")
  const unsafeSerial = await send(unsafe, "HTML is a download", "notify")
  const disguised = await put(Buffer.from("<html><script>globalThis.mediaExecuted=true</script></html>"), "image/png")
  const disguisedSerial = await send(disguised, "HTML labelled as PNG", "notify")
  const declaration = await tool("declare", {token, text:"preparing media fixture redraw"})
  if (!declaration.slot_id) throw new Error("setup: media redraw declaration was refused")

  await page.context().addInitScript(() => {
    const probe = {created: [] as {url:string,type:string,size:number}[], revoked: [] as string[]}
    ;(globalThis as any).__dibsMediaProbe = probe
    const create = URL.createObjectURL.bind(URL), revoke = URL.revokeObjectURL.bind(URL)
    URL.createObjectURL = (blob: Blob | MediaSource) => {
      const url = create(blob)
      probe.created.push({url,type: blob instanceof Blob ? blob.type : "media-source",size: blob instanceof Blob ? blob.size : 0})
      return url
    }
    URL.revokeObjectURL = (url: string) => { probe.revoked.push(url); revoke(url) }
  })
  await page.reload({waitUntil:"load"})
  await page.getByRole("searchbox", {name:"Search Dibs"}).fill("")
  await page.locator("#tab-mail").click()
  await page.locator(`#message-${serial}`).waitFor()
  const picture = page.locator(`#message-${serial} .atts img`)
  check("board renders an attached image", await picture.count() === 1)
  if (await picture.count()) {
    await picture.scrollIntoViewIfNeeded()
    // The custom loader may fetch near the clip before the native lazy image
    // decodes. Await that source, settle fonts, then establish the viewport
    // premise again before claiming anything was painted.
    await page.waitForFunction((serial) => !!document.querySelector(`#message-${serial} .atts img`)?.getAttribute("src"), serial)
    await page.evaluate(()=>document.fonts.ready)
    await picture.scrollIntoViewIfNeeded()
    await page.waitForFunction((serial) => {
      const img = document.querySelector(`#message-${serial} .atts img`) as HTMLImageElement
      return img?.complete && img.naturalWidth > 0
    }, serial)
    const painted = await picture.evaluate((img: HTMLImageElement) => ({
      width:img.getBoundingClientRect().width,height:img.getBoundingClientRect().height,
      natural:img.naturalWidth,fit:getComputedStyle(img).objectFit,
      inClip:img.getBoundingClientRect().bottom>img.closest(".view")!.getBoundingClientRect().top && img.getBoundingClientRect().top<img.closest(".view")!.getBoundingClientRect().bottom,
    }))
    check("board image is painted and bounded", painted.inClip && painted.natural === 640 && painted.height > 0 && painted.height <= 240 && painted.fit === "contain", JSON.stringify(painted))
    check("inline image is content rather than a raw blob header",await picture.evaluate(img=>{
      const handle=img.closest("li")!.querySelector(".handle")!,image=img.getBoundingClientRect(),body=img.closest("article")!.querySelector(".body")!.getBoundingClientRect()
      return image.top>=body.bottom&&handle.getBoundingClientRect().width===0
    }))
    check("bounded image has an explicit full-size affordance",(await page.locator(`#message-${serial} [data-media-preview]`).innerText()).includes("View full size"))
    const old = await picture.elementHandle()
    const requests: string[] = []
    page.on("request", request => {if(request.url().includes(`/api/messages/${serial}/attachments/`))requests.push(request.url())})
    const changed = await tool("declare", {token, text:"an unrelated board update while the image is visible",slot_id:declaration.slot_id})
    if (!changed.ok) throw new Error("setup: media redraw declaration update was refused")
    await page.waitForFunction(() => document.body.textContent?.includes("an unrelated board update while the image is visible"))
    check("SSE retains the actual image node", await old!.evaluate((el, serial) => el === document.querySelector(`#message-${serial} .atts img`), serial))
    check("SSE does not refetch the retained image", requests.length === 0, JSON.stringify(requests))
    const opened = page.context().waitForEvent("page")
    await picture.click()
    const full = await opened
    await full.waitForLoadState("load")
    check("full image opens a trusted board document, never a blob document",full.url().startsWith(new URL(page.url()).origin+"/") && new URL(full.url()).searchParams.has("media"))
    const large=full.locator("#media-view .atts img")
    check("trusted full-image document renders its image",await large.count()===1)
    if(await large.count()){
      await full.waitForFunction(()=>{const image=document.querySelector("#media-view .atts img") as HTMLImageElement;return image?.naturalWidth>0})
      check("full-image document offers a larger painted preview",await large.evaluate(el=>el.getBoundingClientRect().height>240))
      check("viewer caption shares one baseline and ordinary spacing",await full.locator("#media-view figcaption").evaluate(caption=>{
        const text=caption.querySelector(".media-caption")!,link=caption.querySelector("a")!,range=document.createRange()
        range.selectNodeContents(text);const a=range.getBoundingClientRect();range.selectNodeContents(link);const b=range.getBoundingClientRect()
        return Math.abs(a.bottom-b.bottom)<=1&&b.left-a.right>0&&b.left-a.right<24
      }))
      if(process.env.DIBS_MEDIA_ARTIFACTS)await full.screenshot({path:join(process.env.DIBS_MEDIA_ARTIFACTS,"board-full-image.png"),animations:"disabled",mask:[full.locator(".who.focal")]})
    }
    await full.close();await page.bringToFront()
    const previousTheme=await page.evaluate(()=>document.documentElement.getAttribute("data-theme"))
    for(const theme of ["dark","light"]){
      await page.evaluate(theme=>{document.documentElement.dataset.theme=theme},theme)
      await page.evaluate(()=>Promise.all(document.getAnimations().filter(animation=>animation.effect?.getComputedTiming().endTime!==Infinity).map(animation=>animation.finished.catch(()=>{}))))
      const nextPage=page.context().waitForEvent("page");await picture.click()
      const themed=await nextPage;await themed.waitForLoadState("load")
      await themed.locator("#media-view .atts img").waitFor()
      const colours=async(target:Page,link:string)=>target.evaluate(selector=>({background:getComputedStyle(document.body).backgroundColor,link:getComputedStyle(document.querySelector(selector)!).color,decoration:getComputedStyle(document.querySelector(selector)!).textDecorationLine}),link)
      const boardColours=await colours(page,".proto"),viewerColours=await colours(themed,"#media-view header a"),downloadColours=await colours(themed,"#media-view a[download]")
      check(`full viewer uses the board's ${theme} background and link style`,boardColours.background===viewerColours.background&&boardColours.link===viewerColours.link&&boardColours.link===downloadColours.link&&viewerColours.decoration===boardColours.decoration&&downloadColours.decoration===boardColours.decoration,JSON.stringify({boardColours,viewerColours,downloadColours}))
      if(process.env.DIBS_MEDIA_ARTIFACTS)await themed.screenshot({path:join(process.env.DIBS_MEDIA_ARTIFACTS,`board-full-image-${theme}.png`),animations:"disabled",mask:[themed.locator(".who.focal")]})
      await themed.close();await page.bringToFront()
    }
    await page.evaluate(theme=>{if(theme===null)document.documentElement.removeAttribute("data-theme");else document.documentElement.setAttribute("data-theme",theme)},previousTheme)
    const originalViewport=page.viewportSize()
    for(const height of [720,1000]){
      await page.setViewportSize({width:1280,height})
      await page.locator(`#message-${serial}`).evaluate(el=>el.scrollIntoView({block:"start"}))
      check(`inline caption stays below the preview and above the fade at ${height}px`,await page.locator(`#message-${serial} figcaption`).evaluate(caption=>{
        const frame=caption.closest("figure")!.querySelector("[data-media-preview]")!.getBoundingClientRect(),label=caption.getBoundingClientRect(),clip=caption.closest("main")!.getBoundingClientRect()
        return label.top>=frame.bottom&&label.bottom<=clip.bottom-20&&label.top>=clip.top&&caption.textContent!.includes("image ·")&&!!caption.querySelector("a[download]")
      }))
    }
    if(originalViewport)await page.setViewportSize(originalViewport)
    if (process.env.DIBS_MEDIA_ARTIFACTS) {
      const viewport=page.viewportSize()
      await page.setViewportSize({width:1280,height:1000})
      await page.locator(`#message-${serial}`).evaluate(el=>el.scrollIntoView({block:"start"}))
      await page.screenshot({path:join(process.env.DIBS_MEDIA_ARTIFACTS,"board-image.png"),
        animations:"disabled",mask:[page.locator("#ctx-you"),page.locator(".who.focal")]})
      if(viewport)await page.setViewportSize(viewport)
    }
  }
  const unsafeRow = page.locator(`#message-${unsafeSerial}`)
  check("HTML has a download control and no embedded document", await unsafeRow.locator("a[download]").count() === 1 && await unsafeRow.locator("img,video,iframe,object").count() === 0)
  const downloadEvent=page.waitForEvent("download")
  await unsafeRow.locator("a[download]").click()
  const download=await downloadEvent
  const downloaded=await download.createReadStream()
  const chunks:Buffer[]=[]
  for await(const chunk of downloaded!)chunks.push(Buffer.from(chunk))
  check("download carries the exact untrusted bytes",Buffer.concat(chunks).equals(Buffer.from("<script>globalThis.mediaExecuted=true</script>")))
  check("download is binary and its URL is immediately revoked",await page.evaluate(()=>{
    const probe=(globalThis as any).__dibsMediaProbe, entry=probe.created.at(-1)
    return entry?.type==="application/octet-stream"&&probe.revoked.includes(entry.url)
  }))
  const bad = page.locator(`#message-${disguisedSerial}`)
  await bad.scrollIntoViewIfNeeded()
  if (await bad.locator("img").count()) {
    await page.waitForFunction((id) => {
      const img = document.querySelector(`#message-${id} .atts img`) as HTMLImageElement
      return !img || img.complete
    }, disguisedSerial)
  }
  check("HTML labelled PNG paints nothing and executes nothing", await bad.locator("img").evaluateAll((imgs:HTMLImageElement[])=>imgs.every(img=>img.naturalWidth===0)) && await page.evaluate(()=>!(globalThis as any).mediaExecuted))

  const box = page.locator(".compose.new-msg")
  const picker = box.locator('input[type="file"]')
  check("new-message composer has a multiple-file picker", await picker.count() === 1 && await picker.getAttribute("multiple") !== null)
  const reply = page.locator(`.compose.reply[data-serial="${serial}"]`)
  check("human reply composer has an attachment picker", await reply.locator('input[type="file"]').count() === 1)
  if (await picker.count()) {
    const dir = mkdtempSync(join(tmpdir(), "dibs-media-files-"))
    try {
      const file = join(dir,"concept.png"), note = join(dir,"notes.txt")
      await Bun.write(file,Buffer.from(fixture,"base64"));await Bun.write(note,"fixture notes")
      await picker.setInputFiles([file,note])
      check("both selected files have removable name/size chips", await box.locator("[data-file-chip]").count()===2 && (await box.innerText()).includes("concept.png") && (await box.innerText()).includes("notes.txt"))
      await box.getByRole("button",{name:"Remove notes.txt",exact:true}).click()
      check("file removal keeps the other selection", await box.locator("[data-file-chip]").count()===1)
      await page.locator("#msg-to").fill(recipient)
      await page.locator("#msg-body").fill("An attached concept from the board")
      const original = await picker.elementHandle()
      const changed = await tool("declare",{token,text:"file draft must survive this redraw",slot_id:declaration.slot_id})
      if (!changed.ok) throw new Error("setup: file redraw declaration update was refused")
      await page.waitForFunction(()=>document.body.textContent?.includes("file draft must survive this redraw"))
      check("SSE preserves pending files and picker identity", await original!.evaluate(el=>el.isConnected) && await box.locator("[data-file-chip]").count()===1)
      const sentResponse=page.waitForResponse(response=>response.url().endsWith("/api/act/send")&&response.request().method()==="POST")
      await box.locator(".act.send").click()
      const sent=await(await sentResponse).json()
      await page.waitForFunction(()=>document.querySelector("#flash")?.textContent?.includes("sent to"))
      check("new board message reaches the agent",!!sent.msg_serial)
      if(sent.msg_serial){
        const full = await tool("read_mail",{token,msg_serial:sent.msg_serial,include_body:true})
        check("new board message carries the exact selected attachment",full.message?.attachments?.length===1 && full.message.attachments[0].blob===image)
      }
      // Private synthetic drag/drop and paste events: never read the person's
      // actual clipboard. The reply goes through the ordinary upload and act.
      await reply.locator('input[type="text"]').fill("Reply with concept files")
      await reply.evaluate((box,png)=>{
        const bytes=Uint8Array.from(atob(png),c=>c.charCodeAt(0))
        const drop=new DataTransfer();drop.items.add(new File(["drop notes"],"dropped.txt",{type:"text/plain"}))
        box.dispatchEvent(new DragEvent("drop",{bubbles:true,cancelable:true,dataTransfer:drop}))
        const paste=new DataTransfer();paste.items.add(new File([bytes],"pasted.png",{type:"image/png"}))
        box.dispatchEvent(new ClipboardEvent("paste",{bubbles:true,cancelable:true,clipboardData:paste}))
      },fixture)
      check("reply accepts file drop and image paste",await reply.locator("[data-file-chip]").count()===2 && (await reply.innerText()).includes("pasted.png"))
      await reply.getByRole("button",{name:"Remove dropped.txt",exact:true}).click()
      await reply.locator(".act.respond").click()
      await page.waitForFunction(()=>document.querySelector("#flash")?.textContent?.includes("answered"))
      const answered=await tool("read_mail",{token,msg_serial:serial,include_body:true})
      check("reply text is recorded",answered.message?.response==="Reply with concept files")
      const files:any[]=[]
      let cursor:string|undefined
      do{
        const after=await tool("inbox",{token,...(cursor?{cursor}:{})})
        files.push(...(after.messages??[]).filter((message:any)=>message.body===`The file attached to my reply to #${serial}.`))
        cursor=after.next_cursor
      }while(cursor)
      const fileMail=files.length===1?await tool("read_mail",{token,msg_serial:files[0].serial,include_body:true}):null
      check("reply attachment arrives as ordinary following mail",files.length===1&&fileMail?.message?.attachments?.length===1&&fileMail.message.attachments[0].blob===image,JSON.stringify(files))
    } finally {rmSync(dir,{recursive:true,force:true})}
  }
  await checkMediaLifetime(page,check,tool,token,put,send,image)
}

async function checkMediaLifetime(page:Page,check:Check,tool:Tool,token:string,
  put:(data:Buffer,mime:string)=>Promise<string>,send:(blob:string,body:string,type?:string)=>Promise<number>,image:string){
  const videoBytes=await page.evaluate(async()=>{
    const canvas=document.createElement("canvas");canvas.width=64;canvas.height=32
    const stream=canvas.captureStream(0),track=stream.getVideoTracks()[0] as CanvasCaptureMediaStreamTrack
    const recorder=new MediaRecorder(stream,{mimeType:"video/webm"}),chunks:Blob[]=[]
    try{
      const stopped=new Promise<Blob>((resolve,reject)=>{
        recorder.ondataavailable=e=>chunks.push(e.data)
        recorder.onerror=()=>reject(new Error("video fixture recording failed"))
        recorder.onstop=()=>resolve(new Blob(chunks,{type:"video/webm"}))
      })
      recorder.start();canvas.getContext("2d")!.fillRect(0,0,64,32);track.requestFrame()
      // Give the encoder a nonzero time span and a second frame; an immediate
      // stop can produce a WebM header with no video frame at all.
      await new Promise(resolve=>setTimeout(resolve,100))
      canvas.getContext("2d")!.fillStyle="white";canvas.getContext("2d")!.fillRect(0,0,32,32);track.requestFrame()
      recorder.stop()
      return Array.from(new Uint8Array(await(await stopped).arrayBuffer()))
    }finally{stream.getTracks().forEach(track=>track.stop())}
  })
  if(!videoBytes.length)throw new Error("setup: empty video fixture")
  const clip=await put(Buffer.from(videoBytes),"video/webm")
  const video=await send(clip,"video is fetched on Play","notify")
  const oversized=await send(clip,"oversized metadata fixture","notify")
  const expired=await send(image,"expired metadata fixture","notify")
  const registryOnly=await send(image,"response type is authoritative","notify")
  const many:number[]=[]
  for(let i=0;i<9;i++)many.push(await send(image,`bounded preview ${i}`,"notify"))
  // Real mail/server authority, with two explicitly synthetic presentation
  // states. The real eviction and upload cap are tested at the guarded mux.
  await page.route("**/api/messages",async route=>{
    const response=await route.fetch(),json=await response.json()
    for(const message of json.messages??[]){
      if(message.serial===oversized)message.attachments[0].size=Number.MAX_SAFE_INTEGER
      if(message.serial===expired)message.attachments[0].expired=true
    }
    await route.fulfill({response,json})
  })
  await page.route(`**/api/messages/${registryOnly}/attachments/0`,async route=>{
    const response=await route.fetch()
    await route.fulfill({response,headers:{...response.headers(),"content-type":"application/octet-stream","x-dibs-media-kind":""}})
  })
  const reads:number[]=[]
  page.on("request",request=>{const match=request.url().match(/\/api\/messages\/(\d+)\/attachments\//);if(match)reads.push(Number(match[1]))})
  await page.reload({waitUntil:"load"});await page.locator("#tab-mail").click()
  await page.locator(`#message-${expired}`).waitFor()
  const ordinary=page.locator(`#message-${video} .atts video`)
  await ordinary.scrollIntoViewIfNeeded()
  check("video has controls and waits for Play before fetching",await ordinary.getAttribute("controls")!==null&&await ordinary.getAttribute("preload")==="metadata"&&!reads.includes(video))
  await page.locator(`#message-${video} [data-media-play]`).click()
  await page.waitForFunction(id=>(document.querySelector(`#message-${id} video`) as HTMLVideoElement)?.videoWidth>0,video)
  check("Play loads real video metadata",reads.filter(id=>id===video).length===1&&await ordinary.evaluate((video:HTMLVideoElement)=>video.videoWidth===64))
  await ordinary.evaluate((video:HTMLVideoElement)=>video.pause())
  await page.locator(`#message-${oversized} [data-media-play]`).click()
  await page.waitForFunction(id=>["failed","ready"].includes(document.querySelector(`#message-${id} [data-media-key]`)?.getAttribute("data-media-state")||""),oversized)
  check("oversized video is refused before any fetch",!reads.includes(oversized)&&(await page.locator(`#message-${oversized} .media-caption`).innerText()).toLowerCase().includes("open/download"))
  const gone=page.locator(`#message-${expired}`);await gone.scrollIntoViewIfNeeded()
  check("expired attachment is a marker with no fetch or broken image",await gone.locator(".attachment-expired").count()===1&&await gone.locator(".attachment-expired").innerText()==="expired"&&await gone.locator("img,video").count()===0&&!reads.includes(expired))
  await page.locator(`#message-${registryOnly}`).scrollIntoViewIfNeeded()
  await page.waitForFunction(id=>["failed","ready"].includes(document.querySelector(`#message-${id} [data-media-key]`)?.getAttribute("data-media-state")||""),registryOnly)
  check("server response refuses a sender-typed image Blob",await page.evaluate(()=>!(globalThis as any).__dibsMediaProbe.created.some((entry:any)=>entry.type==="application/octet-stream")))
  for(const id of many){
    const row=page.locator(`#message-${id}`);await row.scrollIntoViewIfNeeded()
    if(await row.locator('[data-media-state="evicted"]').count())await row.locator("[data-media-load]").click()
    await page.waitForFunction(id=>document.querySelector(`#message-${id} [data-media-key]`)?.getAttribute("data-media-state")==="ready",id)
  }
  const live=await page.evaluate(()=>{
    const probe=(globalThis as any).__dibsMediaProbe
    return probe.created.filter((entry:any)=>!probe.revoked.includes(entry.url)).length
  })
  check("live preview URLs are capped at eight",live<=8,`live=${live}`)
  const released=page.locator('[data-media-state="evicted"] [data-media-load]').first()
  check("a released image keeps a way to load it again",await released.count()>0)
  if(await released.count()){
    const id=await released.locator("..").getAttribute("data-serial")
    const parent=page.locator(`#message-${id} [data-media-key]`)
    await released.click()
    await parent.locator('img[src^="blob:"]').waitFor()
    check("released image can be loaded again",await parent.getAttribute("data-media-state")==="ready")
  }
  await page.getByRole("searchbox",{name:"Search Dibs"}).fill("a search that removes every media node")
  await page.waitForFunction(()=>document.querySelectorAll("#pane-mail [data-media-key]").length===0)
  await page.evaluate(()=>new Promise(resolve=>requestAnimationFrame(()=>requestAnimationFrame(resolve))))
  check("removing media rows revokes every remaining URL",await page.evaluate(()=>{
    const probe=(globalThis as any).__dibsMediaProbe
    return probe.created.length>8&&probe.created.every((entry:any)=>probe.revoked.includes(entry.url))
  }))
  await page.unroute("**/api/messages");await page.unroute(`**/api/messages/${registryOnly}/attachments/0`)
  await page.getByRole("searchbox",{name:"Search Dibs"}).fill("")
}

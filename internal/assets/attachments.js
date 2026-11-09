// SPDX-License-Identifier: GPL-3.0-or-later
// Copyright 2026 Agenxy

// Only the authenticated web surface constructs these controllers. The MCP
// panel shares the components without gaining a network or credential route.
class BoardMedia {
  constructor(root, fetcher, limit, notify) {
    this.root = root; this.fetcher = fetcher; this.limit = limit; this.notify = notify
    this.urls = new Map(); this.loading = new Map(); this.observed = new WeakSet()
    this.observer = new IntersectionObserver(entries => {
      for (const entry of entries) if (entry.isIntersecting) {
        this.observer.unobserve(entry.target); void this.load(entry.target)
      }
    }, {rootMargin:"80px"})
    this.changes = new MutationObserver(() => this.cleanup())
    this.changes.observe(root, {childList:true,subtree:true})
    root.addEventListener("click", event => {
      const download = event.target.closest?.("[data-attachment-download]")
      if (download) {event.preventDefault();void this.download(download.closest("[data-media-key]"));return}
      const play = event.target.closest?.("[data-media-play]")
      if (play) {event.preventDefault();void this.load(play.closest("[data-media-key]"),true)}
      const reload = event.target.closest?.("[data-media-load]")
      if (reload) {event.preventDefault();void this.load(reload.closest("[data-media-key]"))}
    })
    addEventListener("pagehide", () => {for(const node of [...this.urls.keys()])this.release(node)})
    addEventListener("pageshow", () => this.sync())
  }
  sync() {
    this.cleanup()
    for (const node of this.root.querySelectorAll('[data-media-key][data-kind="image"]')) {
      if (!this.observed.has(node) && !node.dataset.mediaState) {
        this.observed.add(node);this.observer.observe(node)
      }
    }
  }
  cleanup() {
    for (const node of [...this.urls.keys()]) if (!node.isConnected) this.release(node)
    for (const [node, controller] of this.loading) if (!node.isConnected) {controller.abort();this.loading.delete(node)}
  }
  release(node) {
    const entry = this.urls.get(node)
    if (!entry) return
    const video = node.querySelector("video")
    if (video) {video.pause();video.removeAttribute("src");video.load()}
    node.querySelector("img")?.removeAttribute("src")
    URL.revokeObjectURL(entry.url);this.urls.delete(node)
    node.dataset.mediaState = "evicted"
    const play = node.querySelector("[data-media-play]")
    if (play) play.hidden = false
    const reload = node.querySelector("[data-media-load]")
    if (reload) reload.hidden = false
  }
  room(size) {
    const budget = Math.min(this.limit,64*1024*1024)
    if (size > budget) throw new Error("File is too large to preview here. Open/download a smaller file.")
    const used = () => [...this.urls.values()].reduce((sum,entry)=>sum+entry.size,0)
    while (this.urls.size >= 8 || used()+size > budget) {
      const victim = [...this.urls.keys()].find(node => node.querySelector("video")?.paused !== false)
      if (!victim) throw new Error("Close a playing video before loading another file.")
      this.release(victim)
    }
  }
  path(node) {return `/api/messages/${node.dataset.serial}/attachments/${node.dataset.index}`}
  async bytes(node, binary, signal) {
    if (Number(node.dataset.size) > this.limit) throw new Error("File is too large to preview here. Open/download a smaller file.")
    const response = await this.fetcher(this.path(node), {signal})
    if (!response.ok) {
      const error = await response.json().catch(()=>({}))
      if(response.status===410){node.dataset.mediaState="expired";node.querySelector(".media-caption").textContent="expired"}
      throw new Error(error.hint || "The attachment could not be read. Reopen the board.")
    }
    // Only the registry verdict on the authenticated response types a Blob.
    // Sender metadata cannot turn a download into an origin-bearing document.
    const kind = response.headers.get("X-Dibs-Media-Kind") || ""
    const mime = response.headers.get("Content-Type") || "application/octet-stream"
    const typed = (kind==="image" || kind==="video") && mime.toLowerCase().startsWith(kind+"/")
    if (!binary && (!typed || kind!==node.dataset.kind)) throw new Error("This file is available as a download only.")
    if (Number(response.headers.get("Content-Length")) > this.limit) {
      await response.body?.cancel();throw new Error("File exceeds the attachment limit.")
    }
    const reader = response.body.getReader(), chunks=[]
    let size=0
    for (;;) {
      const {done,value}=await reader.read();if(done)break
      size+=value.byteLength
      if(size>this.limit){await reader.cancel();throw new Error("File exceeds the attachment limit.")}
      chunks.push(value)
    }
    return new Blob(chunks,{type:binary?"application/octet-stream":mime})
  }
  async load(node, play=false) {
    if (!node || node.dataset.expired==="true" || this.loading.has(node)) return
    const current=this.urls.get(node), video=node.querySelector("video")
    if(current){if(play&&video)void video.play().catch(()=>{});return}
    const controller=new AbortController();this.loading.set(node,controller);node.dataset.mediaState="loading"
    try {
      const blob=await this.bytes(node,false,controller.signal)
      if(!node.isConnected)return
      this.room(blob.size)
      const url=URL.createObjectURL(blob);this.urls.set(node,{url,size:blob.size})
      const target=node.querySelector(node.dataset.kind==="image"?"img":"video")
      if(!target){this.release(node);return}
      target.onerror=()=>{
        if(!target.getAttribute("src"))return
        this.release(node);node.dataset.mediaState="failed"
        target.remove();node.querySelector("[data-media-preview]")?.remove()
        node.querySelector("[data-media-load]")?.remove()
        node.querySelector(".media-caption").textContent="Preview unavailable · download the file"
      }
      target.src=url;node.dataset.mediaState="ready"
      const reload=node.querySelector("[data-media-load]");if(reload)reload.hidden=true
      if(play&&video){node.querySelector("[data-media-play]").hidden=true;await video.play().catch(()=>{})}
    } catch(error) {
      if(controller.signal.aborted)return
      if(node.dataset.mediaState!=="expired")node.dataset.mediaState="failed"
      node.querySelector(".media-caption").textContent=error.message
    } finally {this.loading.delete(node)}
  }
  async download(node) {
    if(!node || node.dataset.expired==="true" || this.loading.has(node))return
    const controller=new AbortController();this.loading.set(node,controller)
    try {
      const blob=await this.bytes(node,true,controller.signal)
      if(!node.isConnected)return
      this.room(blob.size)
      const url=URL.createObjectURL(blob), link=document.createElement("a")
      try {
        link.href=url;link.download=`attachment-${node.dataset.serial}-${node.dataset.index}`
        document.body.append(link);link.click()
      } finally {link.remove();URL.revokeObjectURL(url)}
    } catch(error) {if(!controller.signal.aborted)this.notify(error.message,true)}
    finally {this.loading.delete(node)}
  }
}

class BoardFiles {
  constructor(root, fetcher, bytes, count, notify) {
    this.root=root;this.fetcher=fetcher;this.bytes=bytes;this.count=count;this.notify=notify;this.drafts=new Map();this.rendered=new WeakMap()
    root.addEventListener("change",event=>{
      if(event.target.matches?.('[data-files] input[type="file"]')){
        this.add(event.target.closest("[data-files]"),Array.from(event.target.files));event.target.value=""
      }
    })
    root.addEventListener("click",event=>{
      const button=event.target.closest?.("[data-file-remove]")
      if(!button)return
      event.preventDefault();const box=button.closest("[data-files]"),draft=this.state(box)
      if(!draft.busy){draft.files.splice(Number(button.dataset.fileRemove),1);this.show(box,draft)}
    })
    root.addEventListener("dragover",event=>{
      const box=event.target.closest?.("[data-files]")
      if(box&&Array.from(event.dataTransfer.types).includes("Files"))event.preventDefault()
    })
    root.addEventListener("drop",event=>{
      const box=event.target.closest?.("[data-files]")
      if(box&&event.dataTransfer.files.length){event.preventDefault();this.add(box,Array.from(event.dataTransfer.files))}
    })
    root.addEventListener("paste",event=>{
      const box=event.target.closest?.("[data-files]")
      if(!box)return
      const files=Array.from(event.clipboardData.items).filter(item=>item.kind==="file"&&item.type.startsWith("image/")).map(item=>item.getAsFile()).filter(Boolean)
      if(files.length){event.preventDefault();this.add(box,files)}
    })
  }
  static controls(){return '<div class="file-tools"><label class="file-pick">Attach<input type="file" multiple aria-label="Attach files"></label><ul class="file-chips" data-file-chips></ul></div>'}
  key(box){return box.dataset.serial?"reply:"+box.dataset.serial:"new"}
  state(box){
    const key=this.key(box)
    if(!this.drafts.has(key))this.drafts.set(key,{files:[],busy:false,id:Array.from(crypto.getRandomValues(new Uint8Array(16)),b=>b.toString(16).padStart(2,"0")).join("")})
    return this.drafts.get(key)
  }
  add(box,files){
    const draft=this.state(box)
    if(draft.busy)return
    if(draft.files.length+files.length>this.count)return this.notify(`Attach at most ${this.count} files`,true)
    if(files.some(file=>file.size>this.bytes))return this.notify("Choose a smaller file: it exceeds the attachment limit",true)
    draft.files.push(...files);this.show(box,draft)
  }
  show(box,draft){
    const chips=box.querySelector("[data-file-chips]");if(!chips)return
    const previous=this.rendered.get(chips)
    if(!previous || previous.length!==draft.files.length || previous.some((file,index)=>file!==draft.files[index])){
      chips.replaceChildren()
      draft.files.forEach((file,index)=>{
      const chip=document.createElement("li"),label=document.createElement("span"),remove=document.createElement("button")
      chip.dataset.fileChip="";label.textContent=`${file.name} · ${Board.fmtBytes(file.size)}`
      remove.type="button";remove.textContent="×";remove.dataset.fileRemove=index
      remove.setAttribute("aria-label",`Remove ${file.name}`);remove.disabled=draft.busy
      chip.append(label,remove);chips.append(chip)
      })
      this.rendered.set(chips,[...draft.files])
    }
    for(const button of chips.querySelectorAll("button"))button.disabled=draft.busy
    for(const control of box.querySelectorAll("input,select,button")){
      if(draft.busy){
        if(!control.disabled){control.dataset.fileRequestBusy="true";control.disabled=true}
      }else if(control.dataset.fileRequestBusy){delete control.dataset.fileRequestBusy;control.disabled=false}
    }
  }
  sync(messages){
    const pending=new Set(messages.filter(m=>!Board.TERMINAL.has(m.state)).map(m=>"reply:"+m.serial))
    for(const key of this.drafts.keys())if(key!=="new"&&!pending.has(key))this.drafts.delete(key)
    for(const box of this.root.querySelectorAll("[data-files]"))this.show(box,this.state(box))
  }
  async run(box, action){
    const draft=this.state(box);if(draft.busy)return null
    draft.busy=true;this.show(box,draft)
    try {
      const ids=[]
      for(const file of draft.files){
        const response=await this.fetcher("/api/upload",{method:"POST",headers:{"Content-Type":file.type||"application/octet-stream"},body:file})
        const uploaded=await response.json().catch(()=>({}))
        if(!response.ok||!uploaded.blob)throw new Error(uploaded.hint||"The files could not finish uploading; no reply was submitted.")
        ids.push(uploaded.blob)
      }
      const result=await action(ids,draft.id)
      if(result){draft.files=[];this.drafts.delete(this.key(box))}
      return result
    } catch(error){this.notify(error.message,true);return null}
    finally{draft.busy=false;if(box.isConnected)this.show(box,draft)}
  }
}

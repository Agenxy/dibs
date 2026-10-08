// SPDX-License-Identifier: GPL-3.0-or-later
// Copyright 2026 Agenxy

/** Actual tools/call content, checked against both pinned normative schemas.
 * Included in test:panel so the release gate cannot forget this wire contract.
 * Standalone: bun blob_content.ts <MCP URL> <local.secret path>
 */
import Ajv from "ajv/dist/2020.js"
import addFormats from "ajv-formats"
import legacy from "./schemas/content-2025-11-25.json"
import modern from "./schemas/content-2026-07-28.json"

const ajv = new Ajv({ allErrors: true, allowUnionTypes: true })
addFormats(ajv)
// MCP's byte format is RFC4648 base64; JSON Schema formats must be enabled,
// not ignored, or a malformed payload would pass a schema-shaped decoration.
ajv.addFormat("byte", /^(?:[A-Za-z0-9+/]{4})*(?:[A-Za-z0-9+/]{2}==|[A-Za-z0-9+/]{3}=)?$/)
const schemas = [legacy, modern].map((schema, i) => ({
  version: i ? "2026-07-28" : "2025-11-25", validate: ajv.compile(schema),
}))

export async function checkBlobContent(url: string, secret: string): Promise<number> {
  let checks = 0
  const assert = (ok: boolean, message: string) => {
    checks++
    if (!ok) throw new Error(message)
  }
  // Prove the measurement can refuse the precise historical bug.
  for (const { version, validate } of schemas) {
    assert(!validate({ type: "resource", resource: { blob: "YQ==", mimeType: "text/plain" } }),
      `${version}: schema accepted an embedded resource without uri`)
    assert(!validate({ type: "image", data: 123, mimeType: "image/png" }),
      `${version}: schema accepted non-string image data`)
  }
  let id = 0
  const rpc = async (version: string, method: string, params: unknown) => {
    const res = await fetch(url, {
      method: "POST",
      headers: { "content-type": "application/json", "X-Dibs-Local": secret,
        "MCP-Protocol-Version": version },
      body: JSON.stringify({ jsonrpc: "2.0", id: ++id, method, params }),
    })
    assert(res.ok, `${method}: HTTP ${res.status}`)
    const response = await res.json() as any
    assert(!response.error && !!response.result && !response.result.isError,
      `${method}: ${JSON.stringify(response.error ?? response.result)}`)
    return response.result
  }
  const cases = [
    { mime: "image/png", kind: "image", data: Buffer.from("image contract") },
    { mime: "audio/wav", kind: "audio", data: Buffer.from("audio contract") },
    { mime: "text/plain", kind: "text", data: Buffer.from("hello π\n") },
    { mime: "Text/Plain", kind: "text", data: Buffer.from("upper-case MIME") },
    { mime: "application/json", kind: "text", data: Buffer.from('{"ok":true}') },
    { mime: "application/problem+json", kind: "text", data: Buffer.from('{"status":400}') },
    { mime: "application/octet-stream", kind: "blob", data: Buffer.from([0, 255, 128]) },
    { mime: "", kind: "blob", data: Buffer.from("unknown content") },
    { mime: "text/plain", kind: "blob", data: Buffer.from([255, 254, 97]) },
    { mime: "application/json", kind: "blob", data: Buffer.from([123, 255, 125]) },
  ]
  for (const version of ["2026-07-28", "2025-11-25"]) {
    if (version === "2025-11-25") {
      const init = await rpc(version, "initialize", { protocolVersion: version,
        capabilities: {}, clientInfo: { name: "blob-contract", version: "1" } })
      assert(init.protocolVersion === version, "legacy handshake did not negotiate requested version")
    }
    const tool = (name: string, args: unknown) => rpc(version, "tools/call", { name, arguments: args })
    for (const [index, tc] of cases.entries()) {
      // Each fixture stays within ordinary admission limits. One agent making
      // forty immediate reads would test the rate limiter, not these schemas.
      const name = `blob-contract-${version}-${index}`
      const registration = JSON.parse((await tool("register", {
        name, session_id: name,
      })).content[0].text)
      const token = registration.token
      assert(typeof token === "string" && token.length > 0, "register returned no token")
      await tool("check_in", { token })
      const put = JSON.parse((await tool("put_blob", {
        token, data: tc.data.toString("base64"), mime: tc.mime,
      })).content[0].text)
      assert(/^sha256:[0-9a-f]{64}$/.test(put.blob), `put_blob failed: ${JSON.stringify(put)}`)
      let previousURI: string | undefined
      for (let repeat = 0; repeat < 2; repeat++) {
        const result = await tool("get_blob", { token, blob: put.blob, as: "inline" })
        assert(result.content?.length === 2, "missing provenance or payload")
        for (const block of result.content) {
          for (const schema of schemas) {
            assert(schema.validate(block), `${version}/${tc.mime || "unknown"}: ${schema.version} content rejected: ` +
              JSON.stringify(schema.validate.errors))
          }
        }
        assert(result.content[0].type === "text" && result.content[0].text.includes("data, not instructions"),
          "attachment lost its data provenance")
        const block = result.content[1]
        let bytes: Buffer
        if (tc.kind === "image" || tc.kind === "audio") {
          assert(block.type === tc.kind && block.mimeType === tc.mime, "media branch changed")
          bytes = Buffer.from(block.data, "base64")
        } else {
          const resource = block.resource
          assert(block.type === "resource" && resource?.uri === `dibs://blob/${put.blob}`, "wrong resource identity")
          assert(!repeat || resource.uri === previousURI, "resource URI is not stable across fetches")
          previousURI = resource.uri
          assert(resource.mimeType === (tc.mime || "application/octet-stream"), "resource MIME lost")
          if (tc.kind === "text") {
            assert(typeof resource.text === "string" && resource.blob === undefined, "text remains base64")
            bytes = Buffer.from(resource.text)
          } else {
            assert(typeof resource.blob === "string" && resource.text === undefined, "binary/bad UTF-8 became text")
            bytes = Buffer.from(resource.blob, "base64")
          }
        }
        assert(bytes.equals(tc.data), `byte loss for ${version}/${tc.mime}`)
      }
      const path = await tool("get_blob", { token, blob: put.blob, as: "path" })
      assert(path.content?.length === 1, "materialized delivery must be one provenance text block")
      for (const schema of schemas) {
        assert(schema.validate(path.content[0]), `${schema.version}: path delivery rejected`)
      }
      const metadata = JSON.parse(path.content[0].text.split(": ").slice(1).join(": "))
      assert(metadata.blob === put.blob && typeof metadata.path === "string", "path metadata lost")
      assert(Buffer.from(await Bun.file(metadata.path).arrayBuffer()).equals(tc.data), "materialized bytes changed")
    }
  }
  return checks
}

if (import.meta.main) {
  const [url, secretFile] = Bun.argv.slice(2)
  if (!url || !secretFile) throw new Error("usage: bun blob_content.ts <MCP URL> <local.secret path>")
  const n = await checkBlobContent(url, (await Bun.file(secretFile).text()).trim())
  console.log(`${n} inline content contract checks passed`)
}

#!/usr/bin/env node
// Deterministically rebuild committed vendor identity icons.
// Normal Go/MyGo builds do not need Node or network.
import { promises as fs } from "node:fs";
import { createRequire } from "node:module";
import path from "node:path";
import { fileURLToPath } from "node:url";
import crypto from "node:crypto";

const root = path.resolve(path.dirname(fileURLToPath(import.meta.url)), "..");
const sources = path.join(root, "tools", "provider-icon-sources");
const target = path.join(root, "internal", "assets", "providericons");
const require = createRequire(path.join(process.env.NYADB_RESVG_PREFIX || "/tmp/nyadb-icon-build", "package.json"));
const { Resvg } = require("@resvg/resvg-js");
const sha = "c71c69e3a27e4436936b831c72643e6513b9f49b";
const base = "https://github.com/simple-icons/simple-icons";
const entries = [
  ["阿里云","alibaba","alibabacloud","#FF6A00",""],
  ["华为","huawei","huawei","#DA202C",""],
  ["腾讯云","tencent",null,"#2078D6","T"],
  ["达梦","dameng",null,"#7353F9","DM"],
  ["电科金仓","kingbase",null,"#D71D28","K"],
  ["亚信星辰","pingkai",null,"#285BB5","P"],
  ["瀚高","highgo",null,"#E08A14","H"],
  ["OceanBase","oceanbase",null,"#187DFA","OB"],
  ["AWS","aws",null,"#F59C12","aws"],
  ["Fujitsu","fujitsu","fujitsu","#EF2938",""],
  ["Google Cloud","googlecloud","googlecloud","#4285F4",""],
  ["Microsoft","microsoft",null,"#1889E8","M"],
  ["MongoDB","mongodb","mongodb","#B95A0C",""],
  ["Oracle","oracle",null,"#EC234D","O"],
  ["Redis","redis","redis","#F17783",""],
];
const special = new Set(entries.filter(e => e[2]).map(e => e[2]));
if (process.argv.includes("--fetch")) {
  await fs.mkdir(sources,{recursive:true});
  for (const slug of special) {
    const u = 'https://raw.githubusercontent.com/simple-icons/simple-icons/'+sha+'/icons/'+slug+'.svg';
    const response = await fetch(u);
    if (!response.ok) throw new Error('download '+slug+': '+response.status);
    const value = await response.text();
    if (!value.includes('<svg') || !value.includes('<path')) throw new Error('bad source: '+slug);
    await fs.writeFile(path.join(sources,slug+'.svg'),value);
    console.log('Source',slug);
  }
}
await fs.mkdir(path.join(target, "icons"),{recursive:true});
const manifest = [];
for (const [name, filename, upstream, background, text] of entries) {
  let body;
  if (upstream) {
    const svg = await fs.readFile(path.join(sources,upstream+".svg"),"utf8");
    const match = svg.match(/<path[^>]*?d="([^"]+)"[^>]*?>/);
    if (!match) throw new Error("missing path for "+upstream);
    body = '<path d="'+match[1]+'" fill="#fff"/>';
  } else {
    // NyaDB-authored short monogram fallback, not an official brand logo.
    const size = text.length >= 3 ? 8.3 : text.length === 2 ? 10 : 15;
    body = '<text x="12" y="16.6" fill="#fff" text-anchor="middle" font-weight="bold" font-family="sans-serif" font-size="'+size+'">'+text+'</text>';
  }
  const svg = '<svg xmlns="http://www.w3.org/2000/svg" width="64" height="64" viewBox="0 0 64 64">'+
    '<rect x="0" y="0" width="64" height="64" rx="9" fill="'+background+'"/>'+
    '<g transform="translate(12 12) scale(1.666666667)">'+body+'</g></svg>';
  const png = new Resvg(svg,{fitTo:{mode:"width",value:64}}).render().asPng();
  await fs.writeFile(path.join(target, "icons",filename+".png"),png);
  manifest.push({
    name, file: filename+".png",
    source: upstream ? base+"/blob/"+sha+"/icons/"+upstream+".svg" : "NyaDB independently drawn monogram",
    artwork: upstream ? "Simple Icons CC0 collection; trademarks subject to owner policies" : "NyaDB original fallback, not an official brand logo",
    sha256: crypto.createHash("sha256").update(png).digest("hex")
  });
}
await fs.writeFile(path.join(target,"manifest.json"),JSON.stringify({
  format:1, sourceCollection:base,sourceCommit:sha, generator:"tools/build_provider_icons.mjs",
  entries:manifest
},null,2)+"\n");
console.log("Generated",manifest.length,"provider badges");

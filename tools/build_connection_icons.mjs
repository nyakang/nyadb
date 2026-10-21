#!/usr/bin/env node
// Rebuild committed NyaDB connection icon PNGs. This is an optional developer
// tool: normal Go/MyGo builds only use the PNGs committed under internal/assets.
import { createRequire } from "node:module";
import { promises as fs } from "node:fs";
import path from "node:path";
import { fileURLToPath } from "node:url";
import crypto from "node:crypto";

const root = path.resolve(path.dirname(fileURLToPath(import.meta.url)), "..");
const sourceDir = path.join(root, "tools", "connection-icon-sources");
const targetDir = path.join(root, "internal", "assets", "connectionicons");
const iconDir = path.join(targetDir, "icons");
const upstream = "https://github.com/simple-icons/simple-icons";
const upstreamCommit = "c71c69e3a27e4436936b831c72643e6513b9f49b";
const require = createRequire(path.join(
  process.env.NYADB_RESVG_PREFIX || "/tmp/nyadb-icon-build", "package.json"));
const { Resvg } = require("@resvg/resvg-js");

const sources = new Set([
  "mysql", "postgresql", "sqlite", "mariadb", "mongodb",
  "redis", "snowflake", "alibabacloud", "huawei",
]);
const entries = [
  ["MySQL","mysql","mysql","#34C759"],
  ["PostgreSQL","postgresql","postgresql","#4385EE"],
  ["SQL Server","sql-server","sqlserver","#F8AD04"],
  ["Oracle","oracle","oracle","#F60644"],
  ["SQLite","sqlite","sqlite","#4DBFBE"],
  ["MariaDB","mariadb","mariadb","#B68C65"],
  ["MongoDB","mongodb","mongodb","#BE620B"],
  ["Redis","redis","redis","#F5787B"],
  ["Snowflake","snowflake","snowflake","#27B4E0"],
  ["达梦","dameng","dameng","#7353F9"],
  ["OceanBase","oceanbase","oceanbase","#1176FA"],
  ["阿里云 RDS MySQL 版","alibaba-rds-mysql","cloud","#34C759"],
  ["阿里云 RDS PostgreSQL 版","alibaba-rds-postgresql","cloud","#4385EE"],
  ["阿里云 RDS SQL Server 版","alibaba-rds-sqlserver","cloud","#F8AD04"],
  ["阿里云 MongoDB 版","alibaba-mongodb","cloud","#B65C0A"],
  ["阿里云 Redis 版","alibaba-redis","cloud","#F5787B"],
  ["腾讯云 MySQL","tencent-mysql","cloud","#34C759"],
  ["华为云 PostgreSQL","huawei-postgresql","huawei","#4385EE"],
  ["AWS RDS MySQL","aws-rds-mysql","aws","#33C64A"],
  ["Azure SQL","azure-sql","azure","#F8AD04"],
];

function ownShape(symbol) {
  const s = 'fill="none" stroke="#fff" stroke-width="1.9" stroke-linecap="round" stroke-linejoin="round"';
  switch (symbol) {
    case "sqlserver": return '<path fill="none" stroke="#fff" stroke-width="1.4" d="M3 6L15 2 21 7 10 12Z M3 6L4 20 16 16 21 7 M10 12L11 23 19 19 21 7 M4 20L11 23 M8 4L12 9 M13 3L17 8"/>';
    case "oracle": return '<rect x="2" y="7" width="20" height="10" rx="5" fill="none" stroke="white" stroke-width="2.5"/>';
    case "dameng": return '<path d="M3 13C4 7 8 4 14 3c4 0 6 3 7 5-6-2-11 0-12 5 3-3 8-2 10 1-3 5-9 7-14 5" fill="none" stroke="#fff" stroke-width="3" stroke-linecap="round"/>';
    case "oceanbase": return '<text x="12" y="16.4" fill="#fff" font-size="10" text-anchor="middle" font-family="sans-serif" font-weight="bold">OB</text>';
    case "cloud": return '<rect x="2" y="7" width="20" height="10" rx="3" fill="none" stroke="#fff" stroke-width="2.2"/><path d="M5 12h5m4 0h5M9 9l3 3-3 3m6-6-3 3 3 3" fill="none" stroke="#fff" stroke-width="1.7" stroke-linecap="round"/>';
    case "aws": return '<text x="12" y="13.5" fill="#fff" font-family="sans-serif" font-weight="bold" font-size="10" text-anchor="middle">aws</text><path d="M5 16q7 5 14-1" fill="none" stroke="#fff" stroke-width="1.4" stroke-linecap="round"/>';
    case "azure": return '<path fill="#fff" d="M10 2L2 18l7-1 4-9 4 13 5-2-7-17Z"/>';
    default: throw new Error("missing original symbol: " + symbol);
  }
}
function escapeXML(s) {return s.replaceAll("&","&amp;").replaceAll("<","&lt;").replaceAll(">","&gt;");}
async function shape(symbol) {
  if (!sources.has(symbol)) return ownShape(symbol);
  const file = path.join(sourceDir, symbol === "mysql" ? "mysql-devicon.svg" : symbol + ".svg");
  const svg = await fs.readFile(file,"utf8");
  const match = svg.match(/<path[^>]*?d="([^"]+)"[^>]*?>/);
  if (!match) throw new Error("unrecognized upstream SVG for " + symbol);
  return '<path d="' + match[1] + '" fill="#fff"' + (symbol === "mysql" ? ' transform="scale(0.1875)"' : '') + '/>';
}
async function downloadSources() {
  await fs.mkdir(sourceDir,{recursive:true});
  for(const symbol of [...sources].sort()) {
    const url = 'https://raw.githubusercontent.com/simple-icons/simple-icons/'+ upstreamCommit +'/icons/'+symbol+'.svg';
    const response = await fetch(url);
    if (!response.ok) throw new Error(symbol+' unavailable: '+response.status);
    const data = await response.text();
    if (!data.includes('<svg')) throw new Error('bad SVG: '+symbol);
    await fs.writeFile(path.join(sourceDir,symbol+'.svg'),data,'utf8');
    console.log("Source",symbol);
  }
}
if(process.argv.includes("--fetch")) await downloadSources();
await fs.mkdir(iconDir, {recursive:true});
const manifest = [];
for (const [name, file, symbol, background] of entries) {
  const artwork = await shape(symbol);
  // 24-unit upstream icons; centered on a 64px square with colored rounded base.
  const svg = '<svg xmlns="http://www.w3.org/2000/svg" width="64" height="64" viewBox="0 0 64 64">'+
    '<rect width="64" height="64" rx="7" fill="'+background+'"/>'+
    '<g transform="translate(10 10) scale(1.8333333333)">'+artwork+'</g></svg>';
  const png = new Resvg(svg, {fitTo:{mode:"width",value:64}}).render().asPng();
  await fs.writeFile(path.join(iconDir,file+".png"),png);
  manifest.push({name, file:file+'.png',
    source:symbol === "mysql" ? "https://github.com/devicons/devicon/blob/6a37858047b0e8d9c9c0e68f4a071fc410652e89/icons/mysql/mysql-original.svg" : sources.has(symbol) ? upstream+'/blob/'+upstreamCommit+'/icons/'+symbol+'.svg' : "NyaDB independently drawn SVG geometry",
    artwork:symbol === "mysql" ? "Devicon MIT artwork; brand trademark rights retained" : sources.has(symbol) ? "Simple Icons source, CC0 collection; trademark rights retained" : "NyaDB own geometry",
    sha256:crypto.createHash("sha256").update(png).digest("hex")});
}
await fs.writeFile(path.join(targetDir,"manifest.json"),JSON.stringify({
  format:1, sourceCollection:upstream,sourceCommit:upstreamCommit,
  generator:"tools/build_connection_icons.mjs",
  entries:manifest
},null,2)+"\n");
console.log("Generated",manifest.length,"PNG icons and manifest.json");

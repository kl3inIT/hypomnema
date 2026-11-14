import { readFileSync } from "node:fs"
import { crc32, deflateSync } from "node:zlib"

// Files of every kind the preview shows (issue #422), for the Artifact and Files panel tests. The Word document and
// the PDF of several pages are MemoryOS's own fixtures (web/tests/fixtures/generated-files/, the owner's code).

const here = (name: string) => new URL(`./${name}`, import.meta.url)

export const docx = readFileSync(here("bao-cao-q3.docx"))
export const reportPdf = readFileSync(here("tai-chinh-q3.pdf"))

// pdf is a one-page PDF that says its text, with a cross-reference table that is right.
export function pdf(text: string) {
  const objects = [
    "<< /Type /Catalog /Pages 2 0 R >>",
    "<< /Type /Pages /Kids [3 0 R] /Count 1 >>",
    "<< /Type /Page /Parent 2 0 R /MediaBox [0 0 595 842] /Contents 4 0 R /Resources << /Font << /F1 5 0 R >> >> >>",
    `<< /Length ${44 + text.length} >>\nstream\nBT /F1 24 Tf 72 760 Td (${text}) Tj ET\nendstream`,
    "<< /Type /Font /Subtype /Type1 /BaseFont /Helvetica >>",
  ]
  let body = "%PDF-1.4\n"
  const offsets: number[] = []
  objects.forEach((o, i) => {
    offsets.push(body.length)
    body += `${i + 1} 0 obj\n${o}\nendobj\n`
  })
  const xref = body.length
  body += `xref\n0 ${objects.length + 1}\n0000000000 65535 f \n`
  for (const o of offsets) body += `${String(o).padStart(10, "0")} 00000 n \n`
  body += `trailer\n<< /Size ${objects.length + 1} /Root 1 0 R >>\nstartxref\n${xref}\n%%EOF\n`

  return Buffer.from(body, "latin1")
}

// chart is a small chart as a PNG: five bars on white.
export function chart() {
  const width = 240
  const height = 120
  const heights = [40, 70, 55, 90, 105]
  const rows: number[] = []
  for (let y = 0; y < height; y++) {
    rows.push(0)
    for (let x = 0; x < width; x++) {
      const bar = Math.floor(x / 48)
      const inBar = x % 48 > 8 && x % 48 < 40 && height - y <= (heights[bar] ?? 0)
      rows.push(...(inBar ? [91, 91, 214] : [255, 255, 255]))
    }
  }
  const chunk = (type: string, body: Buffer) => {
    const head = Buffer.alloc(4)
    head.writeUInt32BE(body.length)
    const typed = Buffer.concat([Buffer.from(type, "latin1"), body])
    const crc = Buffer.alloc(4)
    crc.writeUInt32BE(crc32(typed))

    return Buffer.concat([head, typed, crc])
  }
  const header = Buffer.alloc(13)
  header.writeUInt32BE(width, 0)
  header.writeUInt32BE(height, 4)
  header.set([8, 2, 0, 0, 0], 8)

  return Buffer.concat([
    Buffer.from([0x89, 0x50, 0x4e, 0x47, 0x0d, 0x0a, 0x1a, 0x0a]),
    chunk("IHDR", header),
    chunk("IDAT", deflateSync(Buffer.from(rows))),
    chunk("IEND", Buffer.alloc(0)),
  ])
}

// A flow as an SVG with a script, which must never run.
export const svg = Buffer.from(
  '<svg xmlns="http://www.w3.org/2000/svg" width="320" height="120" viewBox="0 0 320 120">' +
    '<rect x="8" y="30" width="90" height="60" rx="10" fill="#5b5bd6"/>' +
    '<rect x="222" y="30" width="90" height="60" rx="10" fill="#2f9e5b"/>' +
    '<path d="M100 60 H218" stroke="#6b7080" stroke-width="4"/>' +
    '<text x="53" y="66" font-size="14" text-anchor="middle" fill="#fff">Order</text>' +
    '<text x="267" y="66" font-size="14" text-anchor="middle" fill="#fff">Ship</text>' +
    "<script>parent.document.title='pwned'</script></svg>",
)

// orders is a CSV of orders, a quoted address with commas in it.
export const orders = [
  "Order,Customer,Address,Product,Qty,Total (₫),Date",
  'DH-0001,Minh Phát Co.,"12 Lê Lợi, Quận 1, TP.HCM",Water filter,4,"34,000,000",02/07/2026',
  'DH-0002,Nguyễn Thị Hương,"45 Trần Phú, Hải Châu, Đà Nẵng",Air fryer,2,"4,380,000",05/07/2026',
  'DH-0003,"Siêu thị ""Xanh""","88 Kim Mã, Ba Đình, Hà Nội",Cooling fan,15,"51,750,000",11/07/2026',
  "DH-0004,Trần Văn Bảo,7 Nguyễn Huệ,Rice cooker,1,1290000,12/07/2026",
  "",
].join("\n")

// sheets is a workbook as the control plane reads it: CSV text a sheet, the second past the 1,000 rows shown.
export const sheets = {
  sheets: [
    {
      name: "Revenue Q3",
      csv:
        "Region,July,August,September,Q3 total\n" +
        "Hà Nội,412,436,451,1299\nTP.HCM,388,402,430,1220\nĐà Nẵng,96,101,112,309\n" +
        "Cần Thơ,54,58,61,173\n\nTotal,950,997,1054,3001\n",
      truncated: false,
    },
    {
      name: "Daily log",
      csv: [
        "Day,Orders,Revenue",
        ...Array.from(
          { length: 1200 },
          (_, i) => `${i + 1},${100 + (i % 37)},${(i * 7919) % 100000}`,
        ),
      ].join("\n"),
      truncated: false,
    },
  ],
}

export const markdown = [
  "# Q3 summary",
  "",
  "Revenue rose **12%** on Q2, led by Hà Nội.",
  "",
  "| Region | Q3 total |",
  "| --- | --- |",
  "| Hà Nội | 1,299 |",
  "| TP.HCM | 1,220 |",
  "",
  "```python",
  "total = df.groupby('region')['q3'].sum()",
  "```",
  "",
].join("\n")

export const python = [
  "import pandas as pd",
  "",
  "# Revenue for Q3",
  "df = pd.read_excel('revenue.xlsx', sheet_name='Revenue Q3')",
  "total = df.groupby('Region')['Q3 total'].sum()",
  "print(total.sort_values(ascending=False))",
  "",
].join("\n")

// bigLog is a log past the 1 MiB a text preview reads.
export const bigLog = Array.from(
  { length: 24_000 },
  (_, i) =>
    `2026-11-14T09:${String(i % 60).padStart(2, "0")}:00Z worker ${i % 7} handled request ${i}`,
).join("\n")

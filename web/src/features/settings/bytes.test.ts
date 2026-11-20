import { expect, test } from "vitest"

import { formatSize, parseSize } from "./bytes"

test("a cap is read and written as the owner writes it", () => {
  expect(parseSize("10GiB")).toBe(10 * 2 ** 30)
  expect(parseSize(" 500MB ")).toBe(500e6)
  expect(parseSize("2048")).toBe(2048)
  expect(parseSize("ten")).toBeUndefined()
  expect(parseSize("0")).toBeUndefined()
  expect(formatSize(10 * 2 ** 30)).toBe("10GiB")
  expect(formatSize(1.5 * 2 ** 30)).toBe("1.5GiB")
})

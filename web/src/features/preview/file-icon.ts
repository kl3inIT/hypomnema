// The file icon and extension associations are a subset of
// material-icon-theme@5.40.0's dist/material-icons.json (MIT, Copyright (c) 2025 Material Extensions).
import { file, fileExtensions } from "./material-file-icons.json"

const icons = new Map(Object.entries(fileExtensions))

export function fileIconURL(filename: string) {
  const name = filename.slice(filename.lastIndexOf("/") + 1)
  const dot = name.lastIndexOf(".")
  const extension = dot < 0 ? "" : name.slice(dot + 1).toLowerCase()
  const icon = icons.get(extension) ?? file

  return `${import.meta.env.BASE_URL}material-file-icons/${icon}.svg`
}

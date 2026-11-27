import { fileIconURL } from "./file-icon"

// Decorative because the file name or type is shown beside it.
export function KindIcon({ name, className }: { name: string; className?: string }) {
  return <img src={fileIconURL(name)} alt="" className={className ?? "size-4"} />
}

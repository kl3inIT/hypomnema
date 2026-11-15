import { toast } from "@/components/ui/toast"

// said tells the owner, in a toast, that a change he made was done. Omnigent, Orca, Infisical and Coder confirm a
// change the same way (sonner); this is the toast of the project's own component style.
export function said(what: string) {
  toast.add({ title: what, type: "success" })
}

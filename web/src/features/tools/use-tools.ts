import { useQuery } from "@tanstack/react-query"

import "@/api/client"
import { listToolsOptions } from "@/api/generated/@tanstack/react-query.gen"

// useToolFamilies are Hypomnema's tools by family (issue #536), which change only with a release of the control plane.
export function useToolFamilies() {
  return useQuery({ ...listToolsOptions(), select: (page) => page.data, staleTime: Infinity })
}

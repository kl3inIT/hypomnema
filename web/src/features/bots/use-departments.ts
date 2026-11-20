import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query"

import "@/api/client"
import {
  listDepartmentsOptions,
  listDepartmentsQueryKey,
} from "@/api/generated/@tanstack/react-query.gen"
import {
  createDepartment,
  deleteDepartment,
  renameDepartment,
  reorderDepartments,
} from "@/api/generated/sdk.gen"
import type { DepartmentList } from "@/api/generated/types.gen"

import { reorderDepartmentList } from "@/features/shell/bot-rows"

import { useRefreshBots } from "./use-bots"

// The departments the sidebar lists Bots in (issue #471), as OpenMausBot's departments (Apache License 2.0,
// https://github.com/milind-soni/OpenMausBot; src/components/bot-sections.ts and /api/sidebar-sections). The
// owner makes, names, orders and removes them; a change reads the Bots again too, whose department it may change.

// useDepartments reads every department in the owner's order.
export function useDepartments() {
  return useQuery({ ...listDepartmentsOptions(), select: (page) => page.data })
}

// useDepartmentsChanged reads the departments and the Bots again after a change.
function useDepartmentsChanged() {
  const queryClient = useQueryClient()
  const refreshBots = useRefreshBots()

  return async () => {
    void refreshBots()
    await queryClient.invalidateQueries({ queryKey: listDepartmentsQueryKey() })
  }
}

// useCreateDepartment makes a department, last, with the Bots named in it ("New department…" on a Bot's menu).
export function useCreateDepartment() {
  const changed = useDepartmentsChanged()

  return useMutation({
    mutationFn: async ({ name, bots }: { name: string; bots: string[] }) =>
      (await createDepartment({ body: { name, bot_ids: bots } })).data,
    onSuccess: changed,
  })
}

// useRenameDepartment gives a department another name; its Bots stay in it.
export function useRenameDepartment() {
  const changed = useDepartmentsChanged()

  return useMutation({
    mutationFn: async ({ id, name }: { id: string; name: string }) =>
      (await renameDepartment({ path: { department_id: id }, body: { name } })).data,
    onSuccess: changed,
  })
}

// useDeleteDepartment removes a department; its Bots go back to the Bots in none.
export function useDeleteDepartment() {
  const changed = useDepartmentsChanged()

  return useMutation({
    mutationFn: async (id: string) => {
      await deleteDepartment({ path: { department_id: id } })
    },
    onSuccess: changed,
  })
}

// useReorderDepartments moves the departments named, shown before the server answers and put back when it refuses,
// as the Bots' order is (use-bots.ts useBotsChange).
export function useReorderDepartments() {
  const queryClient = useQueryClient()
  const key = listDepartmentsQueryKey()

  return useMutation({
    mutationFn: async (ids: string[]) => {
      await reorderDepartments({ body: { department_ids: ids } })
    },
    onMutate: async (ids: string[]) => {
      await queryClient.cancelQueries({ queryKey: key, exact: true })
      const had = queryClient.getQueryData<DepartmentList>(key)
      if (had) queryClient.setQueryData(key, { ...had, data: reorderDepartmentList(had.data, ids) })

      return { had }
    },
    onError: (_error, _ids, context) => {
      if (context?.had) queryClient.setQueryData(key, context.had)
    },
    onSettled: () => queryClient.invalidateQueries({ queryKey: key }),
  })
}

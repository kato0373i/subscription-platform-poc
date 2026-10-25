// 書き込みの共通処理：成功はトースト、項目のエラーはフォームへ、それ以外はトーストでエラーを出す。
import type { UseFormReturnType } from "@mantine/form";
import { notifications } from "@mantine/notifications";
import { useMutation, useQueryClient, type QueryKey } from "@tanstack/react-query";
import { ApiError } from "../api/client.ts";
import { errorMessage, fieldErrors } from "../api/messages.ts";

export function notifyError(e: unknown) {
  const corr = e instanceof ApiError ? e.correlationId : null;
  notifications.show({ color: "red", title: errorMessage(e), message: corr ? `問い合わせ番号：${corr}` : undefined, autoClose: 8000 });
}

export function notifyOk(message: string) {
  notifications.show({ color: "green", message });
}

/** 書き込みの mutation（成功で invalidate とトースト。form を渡すと項目のエラーをフォームに出す）。 */
export function useWrite<V, R>(o: {
  fn: (v: V) => Promise<R>;
  invalidate?: QueryKey[];
  success?: string | ((r: R) => string);
  form?: UseFormReturnType<any>; // eslint-disable-line @typescript-eslint/no-explicit-any
  onSuccess?: (r: R) => void;
  /** API の項目名 → フォームの項目名（例：discount.value → value）。 */
  fieldMap?: Record<string, string>;
}) {
  const qc = useQueryClient();
  return useMutation({
    mutationFn: o.fn,
    onSuccess: async (r) => {
      await Promise.all((o.invalidate ?? []).map((k) => qc.invalidateQueries({ queryKey: k })));
      if (o.success) notifyOk(typeof o.success === "function" ? o.success(r) : o.success);
      o.onSuccess?.(r);
    },
    onError: (e) => {
      const fe = Object.fromEntries(Object.entries(fieldErrors(e)).map(([k, v]) => [o.fieldMap?.[k] ?? k, v]));
      if (o.form && Object.keys(fe).length > 0) o.form.setErrors(fe);
      else notifyError(e);
    },
  });
}

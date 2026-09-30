import { createMemo } from "solid-js"
import { useSync } from "../context/sync"

// WHIS: the hosted gateway provider no longer exists, so "connected" simply
// means at least one provider is available.
export function useConnected() {
  const sync = useSync()
  return createMemo(() => sync.data.provider.length > 0)
}

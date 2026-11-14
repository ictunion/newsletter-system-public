// Dates and times as the admin's browser shows them.
export function formatTime(iso: string | null): string {
  return iso ? new Date(iso).toLocaleString() : ''
}

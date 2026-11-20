export function EmptyState({ title }: { title: string }) {
  return (
    <div className="flex flex-col items-center justify-center gap-3 px-4 py-12 text-center">
      <p className="text-muted-foreground text-sm">{title}</p>
    </div>
  )
}

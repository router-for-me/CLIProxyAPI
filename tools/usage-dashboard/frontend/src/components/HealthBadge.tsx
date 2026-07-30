import { cn } from "@/lib/utils";
import { useCollectorHealth } from "@/api/hooks/useCollectorHealth";
import { useT } from "@/stores/languageStore";

interface Props {
  ok: boolean | null;
  lastPollAt: string;
  error?: string;
}

export function HealthBadge(props: Props) {
  const t = useT();
  const dot = props.ok === null
    ? "bg-muted-foreground"
    : props.ok
      ? "bg-green-500"
      : "bg-red-500";
  const label = props.ok === null
    ? t("health.unknown")
    : props.ok
      ? t("health.healthy")
      : `${t("health.degraded")}${props.error ? `: ${props.error}` : ""}`;
  return (
    <span className="inline-flex items-center gap-2 text-xs text-muted-foreground">
      <span className={cn("h-2 w-2 rounded-full", dot)} />
      {label}
    </span>
  );
}

export function HealthBadgeLive() {
  const { data } = useCollectorHealth();
  return (
    <span role="status" aria-live="polite" aria-atomic="true">
      <HealthBadge
        ok={data?.last_poll_ok ?? null}
        lastPollAt={data?.last_poll_at ?? ""}
        error={data?.last_poll_error}
      />
    </span>
  );
}
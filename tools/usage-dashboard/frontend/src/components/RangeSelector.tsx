import { useEffect, useState } from "react";
import { Select, SelectContent, SelectItem, SelectTrigger, SelectValue } from "@/components/ui/select";
import { Sheet, SheetContent, SheetHeader, SheetTitle } from "@/components/ui/sheet";
import { Input } from "@/components/ui/input";
import { Button } from "@/components/ui/button";
import { useFiltersStore } from "@/stores/filtersStore";
import { useT } from "@/stores/languageStore";

const PRESETS = ["1h", "5h", "24h", "7d", "30d"] as const;

/** Convert an ISO/RFC-3339 timestamp to a `datetime-local` compatible value. */
function toLocalInput(iso: string | undefined): string {
  if (!iso) return "";
  const d = new Date(iso);
  if (Number.isNaN(d.getTime())) return "";
  // YYYY-MM-DDTHH:mm in local time
  const pad = (n: number) => String(n).padStart(2, "0");
  return `${d.getFullYear()}-${pad(d.getMonth() + 1)}-${pad(d.getDate())}T${pad(d.getHours())}:${pad(d.getMinutes())}`;
}

/** Convert a `datetime-local` value to a UTC RFC-3339 string for the API. */
function fromLocalInput(local: string): string | undefined {
  if (!local) return undefined;
  const d = new Date(local);
  if (Number.isNaN(d.getTime())) return undefined;
  return d.toISOString();
}

export function RangeSelector() {
  const range = useFiltersStore((s) => s.range);
  const setRange = useFiltersStore((s) => s.setRange);
  const setExplicitRange = useFiltersStore((s) => s.setExplicitRange);
  const from = useFiltersStore((s) => s.from);
  const to = useFiltersStore((s) => s.to);
  const t = useT();
  const [sheetOpen, setSheetOpen] = useState(false);
  const [startInput, setStartInput] = useState("");
  const [endInput, setEndInput] = useState("");
  const [error, setError] = useState<string | null>(null);

  // Sync local inputs whenever the sheet opens.
  useEffect(() => {
    if (sheetOpen) {
      setStartInput(toLocalInput(from));
      setEndInput(toLocalInput(to || undefined));
      setError(null);
    }
  }, [sheetOpen, from, to]);

  const triggerLabel = range === "explicit" ? t("range.custom") : range;

  const handleSelect = (v: string) => {
    if (v === "__custom__") {
      setSheetOpen(true);
    } else {
      setRange(v as (typeof PRESETS)[number]);
    }
  };

  const handleApply = () => {
    const startIso = fromLocalInput(startInput);
    const endIso = fromLocalInput(endInput);
    if (!startIso || !endIso) {
      setError(t("range.invalid"));
      return;
    }
    if (new Date(startIso).getTime() >= new Date(endIso).getTime()) {
      setError(t("range.invalid"));
      return;
    }
    setExplicitRange(startIso, endIso);
    setSheetOpen(false);
  };

  return (
    <>
      <Select value={range === "explicit" ? "__custom__" : range} onValueChange={(v) => { if (v) handleSelect(v); }}>
        <SelectTrigger className="w-32">
          <SelectValue>{triggerLabel}</SelectValue>
        </SelectTrigger>
        <SelectContent>
          {PRESETS.map((p) => (
            <SelectItem key={p} value={p}>
              {t(`range.${p}`)}
            </SelectItem>
          ))}
          <SelectItem value="__custom__">{t("range.custom")}</SelectItem>
        </SelectContent>
      </Select>

      <Sheet open={sheetOpen} onOpenChange={setSheetOpen}>
        <SheetContent>
          <SheetHeader>
            <SheetTitle>{t("range.custom_range")}</SheetTitle>
          </SheetHeader>
          <div className="mt-4 space-y-4">
            <label className="block space-y-1 text-sm">
              <span className="text-muted-foreground">{t("range.start")}</span>
              <Input
                type="datetime-local"
                value={startInput}
                onChange={(e) => setStartInput(e.target.value)}
              />
            </label>
            <label className="block space-y-1 text-sm">
              <span className="text-muted-foreground">{t("range.end")}</span>
              <Input
                type="datetime-local"
                value={endInput}
                onChange={(e) => setEndInput(e.target.value)}
              />
            </label>
            {error && <p className="text-sm text-destructive">{error}</p>}
            <Button className="w-full" onClick={handleApply}>
              {t("range.apply")}
            </Button>
          </div>
        </SheetContent>
      </Sheet>
    </>
  );
}

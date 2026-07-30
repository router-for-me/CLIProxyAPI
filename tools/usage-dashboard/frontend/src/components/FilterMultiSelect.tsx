import { useState, useRef, useEffect } from "react";
import { Checkbox } from "@/components/ui/checkbox";
import { cn } from "@/lib/utils";

interface Option { label: string; value: string; }
interface Props {
  label: string;
  options: Option[];
  selected: string[];
  onToggle: (v: string) => void;
}

export function FilterMultiSelect({ label, options, selected, onToggle }: Props) {
  const [open, setOpen] = useState(false);
  const ref = useRef<HTMLDivElement>(null);
  useEffect(() => {
    function onDocClick(e: MouseEvent) {
      if (ref.current && !ref.current.contains(e.target as Node)) setOpen(false);
    }
    document.addEventListener("mousedown", onDocClick);
    return () => document.removeEventListener("mousedown", onDocClick);
  }, []);

  return (
    <div className="relative" ref={ref}>
      <button type="button"
              className="border border-border px-3 py-1 text-sm rounded bg-card cursor-pointer"
              onClick={() => setOpen(!open)}
              aria-expanded={open}
              aria-haspopup="listbox">
        {label}{selected.length > 0 && ` (${selected.length})`}
      </button>
      {open && (
        <div className="absolute z-10 mt-1 max-h-64 overflow-y-auto bg-card border border-border rounded shadow-lg p-2 min-w-40"
             role="listbox">
          {options.map((o) => (
            <label key={o.value}
                   className={cn(
                     "flex items-center gap-2 px-2 py-1 text-sm rounded cursor-pointer hover:bg-muted",
                     selected.includes(o.value) && "bg-muted/50",
                   )}
                   role="option"
                   aria-selected={selected.includes(o.value)}>
              <Checkbox checked={selected.includes(o.value)}
                        onCheckedChange={() => onToggle(o.value)} />
              {o.label}
            </label>
          ))}
        </div>
      )}
    </div>
  );
}
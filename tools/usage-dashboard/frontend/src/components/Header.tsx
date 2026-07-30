import { useQueryClient } from "@tanstack/react-query";
import { RefreshCw } from "lucide-react";
import { NavLink } from "react-router-dom";
import { RangeSelector } from "./RangeSelector";
import { Button } from "./ui/button";
import { HealthBadgeLive } from "./HealthBadge";
import { LanguageSwitcher } from "./LanguageSwitcher";
import { useT } from "@/stores/languageStore";

export function Header() {
  const queryClient = useQueryClient();
  const t = useT();
  return (
    <header className="border-b border-border bg-card">
      <div className="mx-auto flex max-w-7xl items-center gap-4 px-4 py-3">
        <h1 className="text-sm font-semibold">{t("app.title")}</h1>
        <nav className="flex gap-2">
          <NavLink
            to="/"
            end
            className={({ isActive }) =>
              `px-2 py-1 rounded ${isActive ? "bg-primary text-primary-foreground" : "text-muted-foreground"}`
            }
          >
            {t("nav.overview")}
          </NavLink>
          <NavLink
            to="/usage"
            className={({ isActive }) =>
              `px-2 py-1 rounded ${isActive ? "bg-primary text-primary-foreground" : "text-muted-foreground"}`
            }
          >
            {t("nav.usage")}
          </NavLink>
        </nav>
        <div className="ml-auto flex items-center gap-2">
          <HealthBadgeLive />
          <RangeSelector />
          <LanguageSwitcher />
          <Button
            variant="outline"
            size="icon"
            onClick={() => queryClient.invalidateQueries()}
            title={t("action.refresh_data")}
          >
            <RefreshCw className="h-4 w-4" />
          </Button>
        </div>
      </div>
    </header>
  );
}
"use client";

import { useState, type FormEvent } from "react";
import { useRouter } from "next/navigation";
import { useAuth } from "@/lib/auth-context";
import { Button } from "@/components/ui/button";
import {
  Card,
  CardContent,
  CardDescription,
  CardHeader,
  CardTitle,
} from "@/components/ui/card";
import { Input } from "@/components/ui/input";
import { Field, FieldLabel } from "@/components/ui/field";
import { Alert, AlertDescription } from "@/components/ui/alert";

export default function LoginPage() {
  const [key, setKey] = useState("");
  const [isSubmitting, setIsSubmitting] = useState(false);
  const [localError, setLocalError] = useState<string | null>(null);
  const { login } = useAuth();
  const router = useRouter();

  async function handleSubmit(e: FormEvent) {
    e.preventDefault();
    if (!key.trim()) return;

    setIsSubmitting(true);
    setLocalError(null);

    try {
      await login(key.trim());
      router.push("/dashboard");
    } catch {
      setLocalError("Invalid management key. Please try again.");
    } finally {
      setIsSubmitting(false);
    }
  }

  return (
    <div className="flex min-h-svh items-center justify-center p-4">
      <Card className="w-full max-w-sm">
        <CardHeader className="text-center">
          <CardTitle className="text-xl">
            CLI Proxy API Management
          </CardTitle>
          <CardDescription>
            Enter your management key to continue
          </CardDescription>
        </CardHeader>
        <CardContent>
          <form onSubmit={handleSubmit} className="flex flex-col gap-4">
            {localError && (
              <Alert variant="destructive">
                <AlertDescription>{localError}</AlertDescription>
              </Alert>
            )}
            <Field>
              <FieldLabel htmlFor="mgmt-key">Management Key</FieldLabel>
              <Input
                id="mgmt-key"
                type="password"
                placeholder="Enter management key"
                value={key}
                onChange={(e) => setKey(e.target.value)}
                disabled={isSubmitting}
                autoComplete="current-password"
              />
            </Field>
            <Button type="submit" disabled={isSubmitting || !key.trim()}>
              {isSubmitting ? "Verifying..." : "Sign In"}
            </Button>
          </form>
        </CardContent>
      </Card>
    </div>
  );
}

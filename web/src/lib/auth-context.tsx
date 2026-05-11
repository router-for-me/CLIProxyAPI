"use client";

import {
  createContext,
  useCallback,
  useContext,
  useEffect,
  useState,
  type ReactNode,
} from "react";
import {
  api,
  setManagementKey,
  clearManagementKey,
  hasManagementKey,
  APIError,
} from "@/lib/api";

interface AuthContextValue {
  isAuthenticated: boolean;
  isLoading: boolean;
  login: (key: string) => Promise<void>;
  logout: () => void;
  error: string | null;
}

const AuthContext = createContext<AuthContextValue | null>(null);

export function AuthProvider({ children }: { children: ReactNode }) {
  const [isAuthenticated, setIsAuthenticated] = useState(false);
  const [isLoading, setIsLoading] = useState(true);
  const [error, setError] = useState<string | null>(null);

  useEffect(() => {
    if (hasManagementKey()) {
      api.config
        .getConfig()
        .then(() => {
          setIsAuthenticated(true);
        })
        .catch(() => {
          clearManagementKey();
          setIsAuthenticated(false);
        })
        .finally(() => {
          setIsLoading(false);
        });
    } else {
      setIsLoading(false);
    }
  }, []);

  const login = useCallback(async (key: string) => {
    setError(null);
    try {
      setManagementKey(key);
      await api.config.getConfig();
      setIsAuthenticated(true);
    } catch (err) {
      clearManagementKey();
      setIsAuthenticated(false);
      if (err instanceof APIError) {
        setError(err.message);
      } else if (err instanceof Error) {
        setError(err.message);
      } else {
        setError("Authentication failed");
      }
      throw err;
    }
  }, []);

  const logout = useCallback(() => {
    clearManagementKey();
    setIsAuthenticated(false);
    setError(null);
  }, []);

  return (
    <AuthContext.Provider
      value={{ isAuthenticated, isLoading, login, logout, error }}
    >
      {children}
    </AuthContext.Provider>
  );
}

export function useAuth(): AuthContextValue {
  const ctx = useContext(AuthContext);
  if (!ctx) {
    throw new Error("useAuth must be used within an AuthProvider");
  }
  return ctx;
}

import { createContext, useCallback, useContext, useEffect, useState, type ReactNode } from "react";
import { api, setUnauthenticatedHandler, type Me } from "./api";

interface AuthState {
  me: Me | null;
  ready: boolean;
  login: (token: string) => Promise<void>;
  logout: () => Promise<void>;
  refresh: () => Promise<void>;
}

const Ctx = createContext<AuthState>(null as unknown as AuthState);

export function AuthProvider({ children }: { children: ReactNode }) {
  const [me, setMe] = useState<Me | null>(null);
  const [ready, setReady] = useState(false);

  const refresh = useCallback(async () => {
    try {
      setMe(await api.me());
    } catch {
      setMe(null);
    } finally {
      setReady(true);
    }
  }, []);

  useEffect(() => {
    setUnauthenticatedHandler(() => setMe(null));
    void refresh();
  }, [refresh]);

  const login = async (token: string) => {
    await api.login(token);
    await refresh();
  };
  const logout = async () => {
    await api.logout().catch(() => undefined);
    setMe(null);
  };

  return <Ctx.Provider value={{ me, ready, login, logout, refresh }}>{children}</Ctx.Provider>;
}

export const useAuth = () => useContext(Ctx);

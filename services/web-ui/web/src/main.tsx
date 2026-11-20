import { StrictMode } from "react";
import { createRoot } from "react-dom/client";
import { BrowserRouter, Navigate, Route, Routes } from "react-router-dom";
import { isAdmin } from "./api";
import { AuthProvider, useAuth } from "./auth";
import { Layout } from "./components/Layout";
import { I18nProvider } from "./i18n";
import { Spinner, ToastProvider } from "./components/ui";
import { Access } from "./pages/Access";
import { Audit } from "./pages/Audit";
import { Dashboard } from "./pages/Dashboard";
import { Data } from "./pages/Data";
import { EntryPage } from "./pages/Entry";
import { Health } from "./pages/Health";
import { Kinds } from "./pages/Kinds";
import { Login } from "./pages/Login";
import { NewEntry } from "./pages/NewEntry";
import { Projects } from "./pages/Projects";
import { Review } from "./pages/Review";
import { Search } from "./pages/Search";
import { ThemeProvider } from "./prefs";
import "./styles.css";

function App() {
  const { me, ready } = useAuth();
  if (!ready) return <Spinner />;
  if (!me) return <Login />;
  const admin = isAdmin(me);
  return (
    <Routes>
      <Route element={<Layout />}>
        <Route index element={<Dashboard />} />
        <Route path="review" element={<Review />} />
        <Route path="entries" element={<Search />} />
        <Route path="entries/new" element={<NewEntry />} />
        <Route path="entries/:id" element={<EntryPage />} />
        <Route path="projects" element={<Projects />} />
        <Route path="health" element={<Health />} />
        {admin && <Route path="access" element={<Access />} />}
        {admin && <Route path="audit" element={<Audit />} />}
        {admin && <Route path="kinds" element={<Kinds />} />}
        {admin && <Route path="data" element={<Data />} />}
        <Route path="*" element={<Navigate to="/" replace />} />
      </Route>
    </Routes>
  );
}

createRoot(document.getElementById("root")!).render(
  <StrictMode>
    <I18nProvider>
      <ThemeProvider>
        <BrowserRouter>
          <AuthProvider>
            <ToastProvider>
              <App />
            </ToastProvider>
          </AuthProvider>
        </BrowserRouter>
      </ThemeProvider>
    </I18nProvider>
  </StrictMode>,
);

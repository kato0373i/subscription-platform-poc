// ルーティング（operator-console §5.2）。/ops を basename にする。
import { createBrowserRouter, RouterProvider } from "react-router";
import { AppLayout } from "./components/AppLayout.tsx";
import { RequireAuth } from "./components/guards.tsx";
import { AuditPage } from "./pages/audit/AuditPage.tsx";
import { ForgotPasswordPage } from "./pages/auth/ForgotPasswordPage.tsx";
import { LoginPage } from "./pages/auth/LoginPage.tsx";
import { NewPasswordPage } from "./pages/auth/NewPasswordPage.tsx";
import { BillingRunsPage } from "./pages/billing/BillingRunsPage.tsx";
import { HomePage } from "./pages/HomePage.tsx";
import { NotFoundPage } from "./pages/NotFoundPage.tsx";
import { AttemptDetailPage } from "./pages/payments/AttemptDetailPage.tsx";
import { AttemptsPage } from "./pages/payments/AttemptsPage.tsx";
import { ReconciliationPage } from "./pages/payments/ReconciliationPage.tsx";
import { TenantDetailPage } from "./pages/tenants/TenantDetailPage.tsx";
import { TenantsPage } from "./pages/tenants/TenantsPage.tsx";
import { TenantWebhooksPage } from "./pages/tenants/TenantWebhooksPage.tsx";

const router = createBrowserRouter(
  [
    { path: "/login", element: <LoginPage /> },
    { path: "/login/new-password", element: <NewPasswordPage /> },
    { path: "/forgot-password", element: <ForgotPasswordPage /> },
    {
      element: <RequireAuth />,
      children: [
        {
          element: <AppLayout />,
          children: [
            { path: "/", element: <HomePage /> },
            { path: "/tenants", element: <TenantsPage /> },
            { path: "/tenants/:tenantId", element: <TenantDetailPage /> },
            { path: "/tenants/:tenantId/webhooks", element: <TenantWebhooksPage /> },
            { path: "/payments/attempts", element: <AttemptsPage /> },
            { path: "/payments/attempts/:attemptId", element: <AttemptDetailPage /> },
            { path: "/payments/reconciliation", element: <ReconciliationPage /> },
            { path: "/billing-runs", element: <BillingRunsPage /> },
            { path: "/audit", element: <AuditPage /> },
            { path: "*", element: <NotFoundPage /> },
          ],
        },
      ],
    },
  ],
  { basename: "/ops" },
);

export function App() {
  return <RouterProvider router={router} />;
}

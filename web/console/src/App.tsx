// ルーティング（設計 §5.4）。/console を basename にし、事業者の中の画面は /t/:tenantId/... に置く。
import { lazy, Suspense } from "react";
import { createBrowserRouter, Navigate, RouterProvider } from "react-router";
import { AppLayout } from "./components/AppLayout.tsx";
import { FullLoader, RequireAuth, RequireTenant } from "./components/guards.tsx";
import { ForgotPasswordPage } from "./pages/auth/ForgotPasswordPage.tsx";
import { LoginPage } from "./pages/auth/LoginPage.tsx";
import { NewPasswordPage } from "./pages/auth/NewPasswordPage.tsx";
import { PlanDetailPage } from "./pages/catalog/PlanDetailPage.tsx";
import { ProductDetailPage } from "./pages/catalog/ProductDetailPage.tsx";
import { ProductsPage } from "./pages/catalog/ProductsPage.tsx";
import { CouponsPage } from "./pages/coupons/CouponsPage.tsx";
import { NotFoundPage } from "./pages/NotFoundPage.tsx";
import { ApiKeysPage } from "./pages/settings/ApiKeysPage.tsx";
import { DunningPage } from "./pages/settings/DunningPage.tsx";
import { StaffPage } from "./pages/settings/StaffPage.tsx";
import { SubscriptionDetailPage } from "./pages/subscriptions/SubscriptionDetailPage.tsx";
import { SubscriptionsPage } from "./pages/subscriptions/SubscriptionsPage.tsx";
import { TenantSelectPage } from "./pages/TenantSelectPage.tsx";
import { DeliveryDetailPage } from "./pages/webhooks/DeliveryDetailPage.tsx";
import { EndpointDetailPage } from "./pages/webhooks/EndpointDetailPage.tsx";
import { WebhooksPage } from "./pages/webhooks/WebhooksPage.tsx";

// ダッシュボードはグラフのライブラリが大きいので遅延読み込み（console-dashboard ADR-R05）
const DashboardPage = lazy(() => import("./pages/dashboard/DashboardPage.tsx").then((m) => ({ default: m.DashboardPage })));

const router = createBrowserRouter(
  [
    { path: "/login", element: <LoginPage /> },
    { path: "/login/new-password", element: <NewPasswordPage /> },
    { path: "/forgot-password", element: <ForgotPasswordPage /> },
    {
      element: <RequireAuth />,
      children: [
        { path: "/", element: <Navigate to="/tenants" replace /> },
        { path: "/tenants", element: <TenantSelectPage /> },
        {
          path: "/t/:tenantId",
          element: <RequireTenant />,
          children: [
            {
              element: <AppLayout />,
              children: [
                { index: true, element: <Navigate to="dashboard" replace /> },
                {
                  path: "dashboard",
                  element: (
                    <Suspense fallback={<FullLoader />}>
                      <DashboardPage />
                    </Suspense>
                  ),
                },
                { path: "subscriptions", element: <SubscriptionsPage /> },
                { path: "subscriptions/:id", element: <SubscriptionDetailPage /> },
                { path: "products", element: <ProductsPage /> },
                { path: "products/:productId", element: <ProductDetailPage /> },
                { path: "plans/:planId", element: <PlanDetailPage /> },
                { path: "coupons", element: <CouponsPage /> },
                { path: "webhooks", element: <WebhooksPage tab="endpoints" /> },
                { path: "webhooks/deliveries", element: <WebhooksPage tab="deliveries" /> },
                { path: "webhooks/endpoints/:endpointId", element: <EndpointDetailPage /> },
                { path: "webhooks/deliveries/:deliveryId", element: <DeliveryDetailPage /> },
                { path: "settings/dunning", element: <DunningPage /> },
                { path: "settings/api-keys", element: <ApiKeysPage /> },
                { path: "settings/staff", element: <StaffPage /> },
                { path: "*", element: <NotFoundPage /> },
              ],
            },
          ],
        },
        { path: "*", element: <NotFoundPage /> },
      ],
    },
  ],
  { basename: "/console" },
);

export function App() {
  return <RouterProvider router={router} />;
}

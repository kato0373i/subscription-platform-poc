// ルーティング（consumer-mypage §5.3）。/mypage を basename にする。
import { createBrowserRouter, Navigate, RouterProvider } from "react-router";
import { AccountGate } from "./components/AccountGate.tsx";
import { RequireAuth } from "./components/guards.tsx";
import { Layout } from "./components/Layout.tsx";
import { AccountPage } from "./pages/account/AccountPage.tsx";
import { ConfirmSignupPage } from "./pages/auth/ConfirmSignupPage.tsx";
import { ForgotPasswordPage } from "./pages/auth/ForgotPasswordPage.tsx";
import { LoginPage } from "./pages/auth/LoginPage.tsx";
import { SignupPage } from "./pages/auth/SignupPage.tsx";
import { ChangePaymentMethodPage } from "./pages/payment-methods/ChangePaymentMethodPage.tsx";
import { PaymentMethodsPage } from "./pages/payment-methods/PaymentMethodsPage.tsx";
import { SubscriptionDetailPage } from "./pages/subscriptions/SubscriptionDetailPage.tsx";
import { SubscriptionsPage } from "./pages/subscriptions/SubscriptionsPage.tsx";
import { WithdrawnPage } from "./pages/WithdrawnPage.tsx";

const router = createBrowserRouter(
  [
    { path: "/login", element: <LoginPage /> },
    { path: "/signup", element: <SignupPage /> },
    { path: "/signup/confirm", element: <ConfirmSignupPage /> },
    { path: "/forgot-password", element: <ForgotPasswordPage /> },
    {
      element: <RequireAuth />,
      children: [
        { path: "/withdrawn", element: <WithdrawnPage /> },
        {
          element: <AccountGate />,
          children: [
            {
              element: <Layout />,
              children: [
                { path: "/", element: <SubscriptionsPage /> },
                { path: "/subscriptions/:id", element: <SubscriptionDetailPage /> },
                { path: "/subscriptions/:id/payment-method", element: <ChangePaymentMethodPage /> },
                { path: "/payment-methods", element: <PaymentMethodsPage /> },
                { path: "/account", element: <AccountPage /> },
                { path: "*", element: <Navigate to="/" replace /> },
              ],
            },
          ],
        },
      ],
    },
  ],
  { basename: "/mypage" },
);

export function App() {
  return <RouterProvider router={router} />;
}

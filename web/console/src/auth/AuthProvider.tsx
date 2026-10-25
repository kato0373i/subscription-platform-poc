// 認証（設計 §5.5、ADR-C01・C07）：Amplify Auth（SRP）で Cognito のスタッフのプールにログインする。
// 無操作 8 時間で自動ログアウト（最後の操作の時刻を localStorage に置き、タブ間で共有する。FR-003）。
import { Amplify } from "aws-amplify";
import { confirmResetPassword, confirmSignIn, fetchAuthSession, getCurrentUser, resetPassword, signIn, signOut } from "aws-amplify/auth";
import { createContext, useCallback, useContext, useEffect, useMemo, useRef, useState, type ReactNode } from "react";
import type { ConsoleConfig } from "../config.ts";

export const IDLE_LIMIT_MS = 8 * 60 * 60 * 1000;
const ACTIVITY_KEY = "sp.console.lastActivity";
const ACTIVITY_WRITE_MS = 30_000;

export type LogoutReason = "idle" | "expired" | "manual";

type Status = "loading" | "signedOut" | "signedIn";

interface AuthContextValue {
  status: Status;
  email: string | null;
  login(email: string, password: string): Promise<"done" | "new_password">;
  completeNewPassword(password: string): Promise<void>;
  logout(reason?: LogoutReason): Promise<void>;
  requestReset(email: string): Promise<void>;
  confirmReset(email: string, code: string, password: string): Promise<void>;
  idToken(): Promise<string>;
  lastLogoutReason: LogoutReason | null;
}

const AuthContext = createContext<AuthContextValue | null>(null);

export function configureAmplify(cfg: ConsoleConfig) {
  Amplify.configure({
    Auth: { Cognito: { userPoolId: cfg.cognito.userPoolId, userPoolClientId: cfg.cognito.clientId, ...(cfg.cognito.endpoint ? { userPoolEndpoint: cfg.cognito.endpoint } : {}) } },
  });
}

function readActivity(): number {
  try {
    return Number(localStorage.getItem(ACTIVITY_KEY) ?? "0");
  } catch {
    return 0;
  }
}

function writeActivity(t: number) {
  try {
    localStorage.setItem(ACTIVITY_KEY, String(t));
  } catch {
    /* 保存できない環境では、このタブの中だけで数える */
  }
}

export function AuthProvider({ children }: { children: ReactNode }) {
  const [status, setStatus] = useState<Status>("loading");
  const [email, setEmail] = useState<string | null>(null);
  const [lastLogoutReason, setReason] = useState<LogoutReason | null>(null);
  const lastWrite = useRef(0);

  const logout = useCallback(async (reason: LogoutReason = "manual") => {
    try {
      await signOut({ global: true }); // リフレッシュトークンも失効させる
    } catch {
      await signOut().catch(() => undefined); // 期限切れなどで global が失敗しても、このブラウザの認証情報は消す
    }
    writeActivity(0);
    setEmail(null);
    setReason(reason);
    setStatus("signedOut");
  }, []);

  // 起動時：保存済みのセッションを復元する（無操作 8 時間を超えていれば復元しない）
  useEffect(() => {
    (async () => {
      try {
        const last = readActivity();
        if (last > 0 && Date.now() - last >= IDLE_LIMIT_MS) {
          await logout("idle");
          return;
        }
        const u = await getCurrentUser();
        const s = await fetchAuthSession();
        if (!s.tokens?.idToken) throw new Error("no session");
        setEmail((s.tokens.idToken.payload.email as string | undefined) ?? u.signInDetails?.loginId ?? u.username);
        writeActivity(Date.now());
        setStatus("signedIn");
      } catch {
        setStatus("signedOut");
      }
    })();
  }, [logout]);

  // 操作の記録（間引いて書く）と、1 分ごとの無操作の確認
  useEffect(() => {
    if (status !== "signedIn") return;
    const touch = () => {
      const now = Date.now();
      if (now - lastWrite.current > ACTIVITY_WRITE_MS) {
        lastWrite.current = now;
        writeActivity(now);
      }
    };
    const events = ["pointerdown", "keydown", "wheel", "touchstart"] as const;
    events.forEach((e) => window.addEventListener(e, touch, { passive: true }));
    const timer = window.setInterval(() => {
      const last = readActivity();
      if (last > 0 && Date.now() - last >= IDLE_LIMIT_MS) void logout("idle");
    }, 60_000);
    return () => {
      events.forEach((e) => window.removeEventListener(e, touch));
      window.clearInterval(timer);
    };
  }, [status, logout]);

  const value = useMemo<AuthContextValue>(
    () => ({
      status,
      email,
      lastLogoutReason,
      async login(mail, password) {
        const r = await signIn({ username: mail.trim(), password, options: { authFlowType: "USER_SRP_AUTH" } });
        if (r.nextStep.signInStep === "CONFIRM_SIGN_IN_WITH_NEW_PASSWORD_REQUIRED") {
          setEmail(mail.trim());
          return "new_password";
        }
        if (!r.isSignedIn) throw new Error(`unsupported sign-in step: ${r.nextStep.signInStep}`);
        setEmail(mail.trim());
        writeActivity(Date.now());
        setReason(null);
        setStatus("signedIn");
        return "done";
      },
      async completeNewPassword(password) {
        const r = await confirmSignIn({ challengeResponse: password });
        if (!r.isSignedIn) throw new Error(`unsupported sign-in step: ${r.nextStep.signInStep}`);
        writeActivity(Date.now());
        setReason(null);
        setStatus("signedIn");
      },
      logout,
      async requestReset(mail) {
        await resetPassword({ username: mail.trim() });
      },
      async confirmReset(mail, code, password) {
        await confirmResetPassword({ username: mail.trim(), confirmationCode: code.trim(), newPassword: password });
      },
      async idToken() {
        const s = await fetchAuthSession(); // 期限が近ければリフレッシュトークンで更新する。24 時間を過ぎると失敗する
        const t = s.tokens?.idToken?.toString();
        if (!t) {
          await logout("expired");
          throw new Error("session expired");
        }
        return t;
      },
    }),
    [status, email, lastLogoutReason, logout],
  );
  return <AuthContext.Provider value={value}>{children}</AuthContext.Provider>;
}

export function useAuth(): AuthContextValue {
  const v = useContext(AuthContext);
  if (!v) throw new Error("useAuth outside AuthProvider");
  return v;
}

/** Cognito のエラーを画面の文言にする（アカウントの有無を明かさない。FR-001・FR-002）。 */
export function authErrorMessage(e: unknown): string {
  const name = (e as { name?: string })?.name ?? "";
  const message = (e as { message?: string })?.message ?? "";
  if (/Temporary password has expired/i.test(message)) return "招待の有効期限が切れています。事業者の管理者に再送を依頼してください。";
  switch (name) {
    case "NotAuthorizedException":
    case "UserNotFoundException":
      return "メールアドレスまたはパスワードが正しくありません。";
    case "LimitExceededException":
    case "TooManyRequestsException":
    case "TooManyFailedAttemptsException":
      return "しばらく時間をおいてお試しください。";
    case "InvalidPasswordException":
      return "パスワードは 12 文字以上で、英大文字・英小文字・数字・記号をそれぞれ含めてください。";
    case "CodeMismatchException":
    case "ExpiredCodeException":
      return "確認コードが正しくないか、期限が切れています。";
    case "EmptySignInUsername":
    case "EmptySignInPassword":
      return "メールアドレスとパスワードを入力してください。";
    default:
      return "処理に失敗しました。時間をおいて再度お試しください。";
  }
}

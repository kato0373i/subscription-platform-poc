// 最小の申込ページ（payments.md §7.10、憲章 §3「HTML 1 枚」）。
//   /checkout?plan_id=<id>#token=<consumer の JWT>          新しい申込
//   /checkout?subscription=<id>#token=<consumer の JWT>     支払方法の変更（互換のため残す。メールのリンクはマイページに変えた）
// トークンがなければ、マイページのログイン・登録へ案内し、戻るときにトークンを受け取る（consumer-mypage FR-011）。
// トークンは URL のフラグメント（サーバーに送られない）で受け取り、読んだらアドレスバーから消してメモリにだけ持つ。
(function () {
  "use strict";
  var $ = function (id) { return document.getElementById(id); };
  var params = new URLSearchParams(location.search);
  var planId = params.get("plan_id");
  var subscriptionId = params.get("subscription");
  var accessToken = new URLSearchParams(location.hash.slice(1)).get("token");
  if (location.hash) history.replaceState(null, "", location.pathname + location.search);
  var plan = null;
  var tokenizer = null;

  var messages = {
    payment_declined: "カードがご利用いただけませんでした。別のカードをお試しください。",
    plan_unavailable: "現在お申し込みいただけません。",
    tenant_not_active: "現在お申し込みいただけません。",
    invalid_payment_token: "カード情報を確認できませんでした。",
    provider_unavailable: "ただいま決済を受け付けられません。時間をおいてお試しください。",
    unauthenticated: "ログインが必要です。",
    coupon_invalid: "クーポンを利用できません。"
  };

  function show(status, text) {
    var el = $("result");
    el.dataset.status = status;
    el.textContent = text;
  }

  function yen(n) { return n.toLocaleString("ja-JP") + "円"; }

  function call(method, path, body) {
    var headers = { "Accept": "application/json" };
    if (accessToken) headers["Authorization"] = "Bearer " + accessToken;
    if (body !== undefined) headers["Content-Type"] = "application/json";
    if (method !== "GET") headers["Idempotency-Key"] = crypto.randomUUID();
    return fetch(path, { method: method, headers: headers, body: body === undefined ? undefined : JSON.stringify(body), credentials: "omit", cache: "no-store" })
      .then(function (res) {
        return res.text().then(function (text) {
          var json = null;
          try { json = text ? JSON.parse(text) : null; } catch (e) { json = null; }
          return { status: res.status, body: json };
        });
      });
  }

  function problemText(res) {
    var code = res.body && res.body.code;
    if (code && code.indexOf("coupon_") === 0) return messages.coupon_invalid;
    return messages[code] || "お申し込みを完了できませんでした（" + (code || res.status) + "）。";
  }

  function loadTokenizer() {
    return call("GET", "/v1/checkout/payment-config").then(function (res) {
      var card = res.body && res.body.methods.filter(function (m) { return m.method_type === "card"; })[0];
      if (!card) throw new Error("no card method");
      if (card.provider !== "stub") throw new Error("unsupported provider: " + card.provider); // fincode の JS は将来
      return new Promise(function (resolve, reject) {
        var s = document.createElement("script");
        s.src = "/checkout/stub-tokenizer.js";
        s.onload = function () { resolve(window.SPStubTokenizer); };
        s.onerror = reject;
        document.head.appendChild(s);
      });
    });
  }

  function loadPlan() {
    if (!planId) {
      $("title").textContent = "お支払い方法の変更";
      return Promise.resolve();
    }
    return call("GET", "/v1/checkout/plans/" + encodeURIComponent(planId)).then(function (res) {
      if (res.status !== 200) throw res;
      plan = res.body;
      $("plan-name").textContent = plan.product.name + " / " + plan.name;
      $("plan-price").textContent = yen(plan.amount) + "（税込）/ " + (plan.billing_interval === "year" ? "年" : "月");
      $("plan-trial").textContent = plan.trial_days > 0 ? "無料トライアル " + plan.trial_days + " 日" : "";
    });
  }

  /** カード番号をトークンにし、入力欄を空にする。番号はこの関数の外に出さない。 */
  function takeToken() {
    var number = $("card-number");
    var exp = $("card-exp");
    try {
      return tokenizer.tokenize({ number: number.value, exp: exp.value });
    } finally {
      number.value = "";
      exp.value = "";
    }
  }

  function submit() {
    var btn = $("submit");
    btn.disabled = true;
    show("processing", "処理しています…");
    var tok;
    try {
      tok = takeToken();
    } catch (e) {
      btn.disabled = false;
      show("error", "カード番号と有効期限を確認してください。");
      return;
    }
    call("POST", "/v1/me/payment-methods", { method_type: "card", provider: "stub", token: tok.token })
      .then(function (pm) {
        if (pm.status !== 201) throw pm;
        if (subscriptionId) {
          return call("PUT", "/v1/me/subscriptions/" + encodeURIComponent(subscriptionId) + "/payment-method", { payment_method_id: pm.body.id });
        }
        var coupon = $("coupon").value.trim();
        return call("POST", "/v1/me/subscriptions", {
          tenant_id: plan.tenant_id, plan_id: plan.plan_id, payment_method_id: pm.body.id, coupon_code: coupon || undefined
        });
      })
      .then(function (res) {
        if (res.status === 201 || (subscriptionId && res.status === 200)) {
          show("succeeded", subscriptionId ? "お支払い方法を変更しました。" : "お申し込みが完了しました。");
          return;
        }
        if (res.status === 202) {
          show("processing", "お支払いの確認中です。完了したらメールでお知らせします。");
          return;
        }
        throw res;
      })
      .catch(function (res) {
        btn.disabled = false;
        show("error", res && res.status ? problemText(res) : "通信に失敗しました。");
      });
  }

  document.addEventListener("DOMContentLoaded", function () {
    if (!accessToken) {
      if (!planId) {
        show("error", messages.unauthenticated);
        return;
      }
      // プランの内容は見せ、カードの入力の代わりにログイン・登録への導線を出す
      var back = encodeURIComponent(location.pathname + location.search);
      $("login-link").href = "/mypage/login?return=" + back;
      $("signup-link").href = "/mypage/signup?return=" + back;
      $("card").hidden = true;
      $("extra").hidden = true;
      $("submit").hidden = true;
      $("login").hidden = false;
      loadPlan().catch(function (res) {
        show("error", res && res.status ? problemText(res) : "ページを読み込めませんでした。");
      });
      return;
    }
    Promise.all([loadPlan(), loadTokenizer()])
      .then(function (r) {
        tokenizer = r[1];
        $("submit").disabled = false;
        $("submit").addEventListener("click", submit);
        show("ready", "");
      })
      .catch(function (res) {
        show("error", res && res.status ? problemText(res) : "ページを読み込めませんでした。");
      });
  });
})();

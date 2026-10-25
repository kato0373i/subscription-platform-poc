// StubAdapter 用のトークナイザー（payments.md §4.5 のテストカード）。本物の決済代行の JS の代わりに、ブラウザの中だけで
// カード番号を tok_stub_<scenario>_<last4>_<MMYY>_<rand8> に変換する。番号はどこにも送らない。
(function () {
  "use strict";
  var scenarios = {
    "4242424242424242": "ok",
    "4000000000000002": "decline",
    "4000000000009995": "insufficient",
    "4000000000000069": "expired",
    "4000000000000127": "invalid",
    "4000000000000408": "timeout",
    "4000000000000409": "timeout_fail",
    "4000000000000410": "timeout_lost",
    "4000000000000202": "async_ok",
    "4000000000000203": "async_fail",
    "4000000000000400": "reg_reject"
  };

  function rand8() {
    var chars = "abcdefghijklmnopqrstuvwxyz0123456789";
    var bytes = new Uint8Array(8);
    crypto.getRandomValues(bytes);
    var out = "";
    for (var i = 0; i < bytes.length; i++) out += chars[bytes[i] % chars.length];
    return out;
  }

  /** tokenize({number, exp}) は {token, last4} を返す。形式が正しくなければ例外。 */
  function tokenize(card) {
    var digits = String(card.number || "").replace(/\D/g, "");
    if (digits.length < 12 || digits.length > 19) throw new Error("card_number_invalid");
    var exp = String(card.exp || "").replace(/\D/g, "");
    if (exp.length !== 4) throw new Error("card_exp_invalid");
    var month = Number(exp.slice(0, 2));
    if (month < 1 || month > 12) throw new Error("card_exp_invalid");
    var scenario = scenarios[digits] || "ok";
    var last4 = digits.slice(-4);
    return { token: "tok_stub_" + scenario + "_" + last4 + "_" + exp + "_" + rand8(), last4: last4 };
  }

  window.SPStubTokenizer = { tokenize: tokenize };
})();

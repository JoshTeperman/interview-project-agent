"use strict";
const $ = (id) => document.getElementById(id);
const status = (msg, isError) => { $("status").textContent = msg || ""; $("status").className = isError ? "error" : ""; };

async function post(path, body) {
  const res = await fetch(path, { method: "POST", headers: { "Content-Type": "application/json" }, body: JSON.stringify(body) });
  let data = {};
  try { data = await res.json(); } catch (_) {}
  return { ok: res.ok, data };
}

async function submit(form, path, body, onOk) {
  const btn = form.querySelector("button[type=submit]");
  btn.disabled = true; status("");
  try {
    const { ok, data } = await post(path, body);
    if (ok) onOk(data); else status(data.message || "Something went wrong.", true);
  } catch (_) {
    status("Network error. Check your connection and try again.", true);
  } finally { btn.disabled = false; }
}

$("email-form").addEventListener("submit", (e) => {
  e.preventDefault();
  const email = $("email").value.trim();
  submit(e.target, "/api/auth/request", { email }, (data) => {
    $("email-form").hidden = true; $("code-form").hidden = false;
    $("sent-to").textContent = `If ${email} can sign in to this project, we've sent it a code. It expires in ${Math.round(data.expires_in_seconds / 60)} minutes. Nothing arrived? Check spam, or go back and check the address.`;
    $("code").focus();
  });
});

$("code-form").addEventListener("submit", (e) => {
  e.preventDefault();
  submit(e.target, "/api/auth/verify", { email: $("email").value.trim(), code: $("code").value.trim() },
    () => { window.location.replace("/"); });
});

$("restart").addEventListener("click", () => {
  $("code-form").hidden = true; $("email-form").hidden = false; $("code").value = ""; status("");
});

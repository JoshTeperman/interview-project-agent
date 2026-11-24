"use strict";
fetch("/api/me").then((r) => r.ok ? r.json() : Promise.reject()).then((me) => {
  document.getElementById("who").textContent = `Signed in as ${me.email}.`;
}).catch(() => window.location.replace("/login"));
document.getElementById("logout").addEventListener("click", async () => {
  await fetch("/api/auth/logout", { method: "POST" });
  window.location.replace("/login");
});

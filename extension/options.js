const $ = (id) => document.getElementById(id);

async function load() {
  const s = await chrome.storage.local.get({ port: 47821, token: "" });
  $("port").value = s.port;
  $("token").value = s.token;
}

async function save() {
  const port = Number($("port").value) || 47821;
  const token = $("token").value.trim();
  await chrome.storage.local.set({ port, token });
  $("status").textContent = "Menguji koneksi ke agen...";
  try {
    const res = await fetch(`http://127.0.0.1:${port}/v1/ping`, { headers: { Authorization: `Bearer ${token}` } });
    $("status").textContent =
      res.ok ? "Tersambung ke agen Tunas." :
      res.status === 401 ? "Token salah. Salin ulang dari menu tray." :
      res.status === 403 ? "Agen menolak ekstensi ini (periksa extension.ids di config.yaml)." :
      `Agen menjawab ${res.status}.`;
  } catch {
    $("status").textContent = "Agen tidak terjangkau. Pastikan Tunas berjalan dan \"Catat domain situs\" aktif.";
  }
}

$("save").addEventListener("click", save);
load();

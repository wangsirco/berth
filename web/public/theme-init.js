// Applies the theme before first paint (no flash). A file, not an inline script: the
// panel's Content-Security-Policy allows only same-origin scripts.
try {
  var t = localStorage.getItem('berth.theme') || 'system'
  if (t === 'dark' || (t === 'system' && matchMedia('(prefers-color-scheme: dark)').matches)) document.documentElement.classList.add('dark')
} catch (e) {}

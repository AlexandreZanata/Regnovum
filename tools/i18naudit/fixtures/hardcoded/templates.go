// Package hardcoded is a fixture of the localization scanner: a document that
// hardcodes its language and the words a person reads. It is never compiled
// into anything — the scanner reads it as text, and the repository-wide scan
// skips this directory (see the -skip flag of the command).
package hardcoded

// hardcodedDocument is the shape the prose and language rules must reject.
//
//lint:ignore U1000 o analisador não vê o leitor: o i18naudit lê este literal como documento e é dele que saem os achados da fixture
const hardcodedDocument = `<!DOCTYPE html>
<html lang="pt-BR">
<head>
<title>Redefinir senha — Regnovum</title>
<style>
body { margin-left: 2rem; }
</style>
</head>
<body>
<h1>Redefinir senha</h1>
<p>Informe sua nova senha para continuar.</p>
</body>
</html>`

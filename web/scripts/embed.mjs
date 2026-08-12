// Move the Next.js export to where //go:embed can reach it.
//
// go:embed patterns cannot leave their own package directory — ".." is
// rejected — so the conventional web/out is unusable and the export has to
// land in internal/web/dist instead. Next always writes to out/, so this
// copies rather than configuring a destination.
//
// The .gitkeep is rewritten afterwards on purpose: the directory is otherwise
// gitignored, and go:embed is a compile-time directive, so a fresh clone with
// no dist/ at all fails to BUILD (docs/decisions.md D16). Losing the file to a
// build would show up as a dirty tree and, eventually, as a broken bisect.
import { cp, mkdir, rm, writeFile } from 'node:fs/promises';

const from = new URL('../out/', import.meta.url);
const to = new URL('../../internal/web/dist/', import.meta.url);

await rm(to, { recursive: true, force: true });
await mkdir(to, { recursive: true });
await cp(from, to, { recursive: true });
await writeFile(new URL('.gitkeep', to), '');

console.log('embedded  out/ -> internal/web/dist/');

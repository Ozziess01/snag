// Сборка демо в браузере: node scripts/build-demo.mjs
//
// 1. vite собирает интерфейс и страницу демо в dist/demo;
// 2. страницы переименовываются: демо — index.html, интерфейс — app.html;
// 3. Snag собирается в WebAssembly (GOOS=js GOARCH=wasm) и сжимается gzip;
// 4. рядом кладётся wasm_exec.js из той же версии Go.
//
// Go берётся из PATH или из переменной GO.

import { execFileSync } from 'node:child_process';
import { copyFileSync, readFileSync, renameSync, rmSync, statSync, writeFileSync } from 'node:fs';
import { join, dirname } from 'node:path';
import { fileURLToPath } from 'node:url';
import { gzipSync, constants } from 'node:zlib';

const web = join(dirname(fileURLToPath(import.meta.url)), '..');
const root = join(web, '..');
const out = join(web, 'dist', 'demo');
const go = process.env.GO || 'go';
const run = (cmd, args, opts = {}) => execFileSync(cmd, args, { stdio: 'inherit', ...opts });

run(process.execPath, [join(web, 'node_modules', 'vite', 'bin', 'vite.js'), 'build', '--config', 'vite.demo.config.ts'], { cwd: web });

renameSync(join(out, 'index.html'), join(out, 'app.html'));
renameSync(join(out, 'demo.html'), join(out, 'index.html'));

const wasm = join(out, 'snag.wasm');
run(go, ['build', '-trimpath', '-ldflags=-s -w', '-o', wasm, './cmd/snag-wasm'], {
  cwd: root,
  env: { ...process.env, GOOS: 'js', GOARCH: 'wasm' },
});
const raw = readFileSync(wasm);
const gz = gzipSync(raw, { level: constants.Z_BEST_COMPRESSION });
writeFileSync(wasm + '.gz', gz);
rmSync(wasm);

const goroot = execFileSync(go, ['env', 'GOROOT'], { encoding: 'utf8' }).trim();
copyFileSync(join(goroot, 'lib', 'wasm', 'wasm_exec.js'), join(out, 'wasm_exec.js'));

const mb = (n) => (n / 1e6).toFixed(1) + ' МБ';
console.log(`демо собрано в ${out}: snag.wasm ${mb(raw.length)} → snag.wasm.gz ${mb(statSync(wasm + '.gz').size)}`);

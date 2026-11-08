// Rebuild the browser asset from the exact package version in package-lock.json.
const fs = require('node:fs');
const path = require('node:path');
const pkg = require('markdown-it/package.json');
const out = path.resolve(__dirname, '../static/vendor');
fs.mkdirSync(out, {recursive:true});
const browser = fs.readFileSync(require.resolve('markdown-it/browser'), 'utf8').replace(/^\/\/# sourceMappingURL=.*$/gm, '');
fs.writeFileSync(path.join(out, 'markdown-it.min.js'), `/*! markdown-it ${pkg.version}; licenses: markdown-it.LICENSE.txt */\n` + browser);
const names = ['markdown-it', ...Object.keys(pkg.dependencies)];
const licenses = names.map(name => {
  const dir = path.join(__dirname, 'node_modules', name);
  const meta = JSON.parse(fs.readFileSync(path.join(dir, 'package.json'), 'utf8'));
  const license = fs.readdirSync(dir).find(f => /^licen[sc]e(?:-MIT)?(?:\.md|\.txt)?$/i.test(f));
  if (!license) throw Error('Missing license for ' + name);
  return `${name} ${meta.version}\n${'='.repeat(60)}\n${fs.readFileSync(path.join(dir,license),'utf8')}`;
});
fs.writeFileSync(path.join(out,'markdown-it.LICENSE.txt'), licenses.join('\n\n'));

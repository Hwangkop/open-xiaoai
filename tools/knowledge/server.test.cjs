// 使用临时资料验证边界和协议，不依赖未入库的全书副本。
const {test} = require('node:test');
const assert = require('node:assert/strict');
const fs = require('node:fs');
const os = require('node:os');
const path = require('node:path');
const crypto = require('node:crypto');
const {spawnSync} = require('node:child_process');
const {loadSnapshot, search, read, handle, RuntimeSnapshot} = require('./server.cjs');

function fixture(t) {
  const root = fs.mkdtempSync(path.join(os.tmpdir(), 'xiaoai-book-'));
  t.after(() => fs.rmSync(root, {recursive: true, force: true}));
  const dir = path.join(root, 'books/time-as-a-friend');
  fs.mkdirSync(path.join(dir, 'original'), {recursive: true});
  const body = '# 第三章：管理\n## 记录开销\n### 第三组练习\n第一周晚上回忆，第二周随时记录。\n## 制作专门的下一阶段任务列表\n新鲜主意另记。\n## 长节\n' + Array.from({length: 100}, (_, i) => `第${i}行 😀测试。`).join('\n');
  const bytes = Buffer.from(body);
  fs.writeFileSync(path.join(dir, 'original/Chapter3.md'), bytes);
  fs.writeFileSync(path.join(dir, 'source.json'), JSON.stringify({title: '把时间当作朋友', author: '李笑来', repository: 'https://github.com/xiaolai/time-as-a-friend', revision: 'fixture', files: {'Chapter3.md': {bytes: bytes.length, git_blob_sha1: crypto.createHash('sha1').update(`blob ${bytes.length}\0`).update(bytes).digest('hex')}}}));
  return {root, dir, source: {id: 'time-as-a-friend', name: '把时间当作朋友', author: '李笑来'}};
}

test('中文检索能区分具体练习与同义主题，出处定位固定版本', t => {
  const {root} = fixture(t);
  const snapshot = loadSnapshot(root);
  assert.equal(search(snapshot, '记录开销 第三组练习').matches[0].section_id, 'time-as-a-friend@fixture/Chapter3:3');
  const ideas = search(snapshot, '任务冒出新点子').matches[0];
  assert.equal(ideas.section_id, 'time-as-a-friend@fixture/Chapter3:5');
  assert.match(ideas.source_url, /blob\/fixture\/Chapter3.md#L5$/);
  assert.deepEqual(search(snapshot, 'xyzunmatched').matches, []);
});

test('分页精确拼回原文，含 emoji 及六十行边界，不漏字重复', t => {
  const {root} = fixture(t);
  const snapshot = loadSnapshot(root);
  let text = '', offset = 0, result;
  do {
    result = read(snapshot, 'Chapter3:7', offset, 4000);
    assert.ok(result.text.split('\n').filter(Boolean).length <= 60);
    text += result.text;
    offset = result.next_offset;
  } while (result.has_more);
  assert.equal(text, snapshot.sources.get('time-as-a-friend').byId.get('time-as-a-friend@fixture/Chapter3:7').text);
  let small = '', at = 0, part;
  do {part = read(snapshot, 'Chapter3:7', at, 13);small += part.text;at = part.next_offset;} while(part.has_more);
  assert.equal(small, text);
});

test('拒绝越权路径、非法参数，原文损坏时停止加载', t => {
  const {root, dir} = fixture(t);
  const snapshot = loadSnapshot(root);
  assert.throws(() => read(snapshot, '../../config.yaml'));
  assert.throws(() => read(snapshot, 'Chapter3:3', -1));
  assert.throws(() => read(snapshot, 'Chapter3:3', 0, 4001));
  assert.throws(() => search(snapshot, 'x', 0));
  assert.throws(() => search(snapshot, 'x'.repeat(301)));
  const runtime = new RuntimeSnapshot(root, {writeStatus: false});
  runtime.refresh();
  const denied = handle(runtime, {id: 1, method: 'tools/call', params: {name: 'search_book', arguments: {query: 'test', path: '../../config.yaml'}}});
  assert.equal(denied.result.isError, true);
  fs.appendFileSync(path.join(dir, 'original/Chapter3.md'), '篡改');
  assert.throws(() => loadSnapshot(root), /校验失败/);
});

test('stdio 子进程完成 MCP 初始化、列工具、检索、读原文与错误响应', t => {
  const {root} = fixture(t);
  const requests = [
    {id: 1, method: 'initialize', params: {protocolVersion: '2024-11-05'}},
    {method: 'notifications/initialized'},
    {id: 2, method: 'tools/list'},
    {id: 3, method: 'tools/call', params: {name: 'search_book', arguments: {query: '第三组练习'}}},
    {id: 4, method: 'tools/call', params: {name: 'read_book_section', arguments: {section_id: 'Chapter3:3'}}},
    {id: 5, method: 'tools/call', params: {name: 'read_book_section', arguments: {section_id: '../../secret'}}},
  ];
  const process = spawnSync(global.process.execPath, [path.join(__dirname, 'server.cjs'), '--root', root], {input: requests.map(r => JSON.stringify(r)).join('\n') + '\n', encoding: 'utf8', timeout: 5000});
  assert.equal(process.status, 0, process.stderr);
  assert.equal(process.stderr, '');
  const replies = process.stdout.trim().split('\n').map(JSON.parse);
  assert.equal(replies.length, 5);
  assert.equal(replies[0].result.protocolVersion, '2024-11-05');
  assert.deepEqual(replies[1].result.tools.map(t => t.name), ['search_book', 'read_book_section']);
  assert.equal(JSON.parse(replies[2].result.content[0].text).matches[0].section_id, 'time-as-a-friend@fixture/Chapter3:3');
  assert.match(JSON.parse(replies[3].result.content[0].text).text, /第一周晚上回忆，第二周随时记录/);
  assert.equal(replies[4].result.isError, true);
});

test('registry 多资料 ID 不冲突，按资料筛选，停用后拒绝检索和读取', t => {
  const root = fs.mkdtempSync(path.join(os.tmpdir(), 'xiaoai-registry-'));
  t.after(() => fs.rmSync(root, {recursive: true, force: true}));
  const sources = [];
  for (const [id, title, body] of [
    ['book-a', '资料甲', '# 第一章\n## 同名章节\n甲资料关于记录开销。\n'],
    ['book-b', '资料乙', '# 第一章\n## 同名章节\n乙资料关于记录开销。\n'],
  ]) {
    const directory = path.join(root, 'sources', id, 'versions', 'v1', 'original');
    fs.mkdirSync(directory, {recursive: true});
    const bytes = Buffer.from(body);
    fs.writeFileSync(path.join(directory, 'notes.md'), bytes);
    const manifest = path.join(root, 'sources', id, 'versions', 'v1', 'manifest.json');
    fs.writeFileSync(manifest, JSON.stringify({
      title,
      files: {'notes.md': {bytes: bytes.length, sha256: crypto.createHash('sha256').update(bytes).digest('hex')}},
    }));
    sources.push({
      id, name: title, author: '', enabled: true, published_version: 'v1',
      versions: [{
        id: 'v1', label: '第一版', status: 'ready',
        manifest: `sources/${id}/versions/v1/manifest.json`,
        content_root: `sources/${id}/versions/v1/original`,
      }],
    });
  }
  const registryFile = path.join(root, 'registry.json');
  fs.writeFileSync(registryFile, JSON.stringify({schema_version: 1, generation: 1, sources}));
  const first = loadSnapshot(root);
  const all = search(first, '记录开销');
  assert.equal(all.matches.length, 2);
  assert.notEqual(all.matches[0].section_id, all.matches[1].section_id);
  assert.equal(search(first, '记录开销', 3, 'book-b').matches[0].source_title, '资料乙');

  sources[0].enabled = false;
  fs.writeFileSync(registryFile, JSON.stringify({schema_version: 1, generation: 2, sources}));
  const second = loadSnapshot(root);
  assert.throws(() => search(second, '记录开销', 3, 'book-a'), /没有这份已启用资料/);
  assert.equal(search(second, '记录开销').matches[0].source_id, 'book-b');
});

test('长期 worker 写入可核对的加载代次心跳，管理端请求不冒充 agent 状态', t => {
  const {root} = fixture(t);
  const runtime = new RuntimeSnapshot(root);
  runtime.refresh();
  const statusFile = path.join(root, '.runtime/worker-status.json');
  const status = JSON.parse(fs.readFileSync(statusFile, 'utf8'));
  assert.equal(status.state, 'ok');
  assert.equal(status.registry_generation, 0);
  assert.equal(status.loaded_generation, 0);
  assert.equal(status.active_sources['time-as-a-friend'], 'fixture');

  fs.rmSync(path.join(root, '.runtime'), {recursive: true, force: true});
  spawnSync(global.process.execPath, [path.join(__dirname, 'server.cjs'), '--root', root, '--request'], {
    input: JSON.stringify({action: 'sources'}),
    encoding: 'utf8',
    timeout: 5000,
  });
  assert.equal(fs.existsSync(statusFile), false);
});

test('Markdown Setext 标题也能作为章节，而不是退化成纯文本分块', t => {
  const root = fs.mkdtempSync(path.join(os.tmpdir(), 'xiaoai-setext-'));
  t.after(() => fs.rmSync(root, {recursive: true, force: true}));
  const directory = path.join(root, 'sources/book/versions/v1/original');
  fs.mkdirSync(directory, {recursive: true});
  const text = '<h1>演讲课程</h1>\n\n第一章 当众讲话\n================\n\n开场正文。\n\n第二章 信任听众\n----------------\n\n信任正文。\n';
  const bytes = Buffer.from(text);
  fs.writeFileSync(path.join(directory, 'book.md'), bytes);
  fs.writeFileSync(path.join(root, 'sources/book/versions/v1/manifest.json'), JSON.stringify({
    title: '演讲测试',
    files: {'book.md': {bytes: bytes.length, sha256: crypto.createHash('sha256').update(bytes).digest('hex')}},
  }));
  fs.writeFileSync(path.join(root, 'registry.json'), JSON.stringify({
    schema_version: 1,
    generation: 1,
    sources: [{
      id: 'book', name: '演讲测试', enabled: true, published_version: 'v1',
      versions: [{
        id: 'v1', label: '第一版', status: 'ready',
        manifest: 'sources/book/versions/v1/manifest.json',
        content_root: 'sources/book/versions/v1/original',
      }],
    }],
  }));
  const loaded = loadSnapshot(root).sources.get('book');
  assert.deepEqual(loaded.sections.map(section => section.heading), [
    '演讲课程',
    '演讲课程 / 第一章 当众讲话',
    '演讲课程 / 第二章 信任听众',
  ]);
});

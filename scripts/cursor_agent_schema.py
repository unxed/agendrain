#!/usr/bin/env python3
"""Extract agent.v1 protobuf schemas from an installed Cursor Agent CLI bundle.

Usage: cursor_agent_schema.py <versions/X dir> > internal/drain/cursor_agent_schema.go

The bundled JS (protobuf-es with a compact descriptor format) declares every
message as `class X extends B{... static $(){return["Name|1 field 9|2 f #0?",refs]}}`
and enums as `(0,a.QT)(n.C,c,"Name",[[0,"A"],...],flag)`. Type tokens: scalar
protobuf type numbers (9 string, 5 int32, 13 uint32, 4 uint64, 3 int64, 8 bool,
12 bytes, ...), `#N` = N-th trailing reference (message or enum), suffix `?`
optional, `*` repeated, `k,v` map. A 4th token names a oneof group.
This script resolves the references starting from agent.v1.ToolCall and the
conversation state roots and prints a Go table. It only reads the bundle; run
it by hand, the output is committed.
"""
import re, sys, glob, os

root = sys.argv[1]
MOD = re.compile(r'(?:^|[,{}])"([^"\n]{3,200}\.js)"\(e,t,r\)\{')
CLASS = re.compile(r'class ([\w$]+) extends ([\w$.]+)\{')
SPEC = re.compile(r'static \$\(\)\{return\["([^"]*)"((?:,[\w$.]+)*)\]\}')
ENUM = re.compile(r'([\w$]+)=\(0,([\w$]+)\.QT\)\(([\w$]+)\.C,([\w$]+),"([\w.]+)",(\[\[.*?\]\]),\d\)')
EXPORT = re.compile(r'r\.d\(t,\{([^}]*)\}\)')
IMPORT = re.compile(r'([\w$]+)=r\("([^"]+)"\)')

msgs = {}
enums = {}
exports = {}
imports = {}
for f in sorted(glob.glob(os.path.join(root, '*.js'))):
    s = open(f, errors='replace').read()
    starts = [(m.start(), m.group(1)) for m in MOD.finditer(s)]
    for i, (pos, name) in enumerate(starts):
        end = starts[i + 1][0] if i + 1 < len(starts) else len(s)
        body = s[pos:end]
        key = f + '::' + name
        ex = {}
        for m in EXPORT.finditer(body[:6000]):
            for part in m.group(1).split(','):
                mm = re.match(r'\s*([\w$]+):\(\)=>([\w$]+)\s*$', part)
                if mm:
                    ex[mm.group(1)] = mm.group(2)
        exports[key] = ex
        imports[key] = {v: f + '::' + p for v, p in IMPORT.findall(body)}
        for m in SPEC.finditer(body):
            cm = list(CLASS.finditer(body[:m.start()]))
            if not cm:
                continue
            ident = cm[-1].group(1)
            refs = [x for x in m.group(2).split(',') if x]
            msgs[(key, ident)] = {'spec': m.group(1), 'refs': refs, 'module': key}
        for m in ENUM.finditer(body):
            vals = re.findall(r'\[(\d+),"(\w+)"\]', m.group(6))
            enums[(key, m.group(1))] = (m.group(5), [(int(a), b) for a, b in vals])


def resolve(module, ref, seen=0):
    if seen > 8:
        return None
    if '.' in ref:
        var, exp = ref.split('.', 1)
        tgt = imports.get(module, {}).get(var)
        if not tgt:
            return None
        ident = exports.get(tgt, {}).get(exp)
        if not ident:
            return None
        return resolve(tgt, ident, seen + 1)
    if (module, ref) in msgs:
        return ('m', (module, ref))
    if (module, ref) in enums:
        return ('e', (module, ref))
    return None


SCALAR = {'1': 'double', '2': 'float', '3': 'int64', '4': 'uint64', '5': 'int32', '8': 'bool',
          '9': 'string', '12': 'bytes', '13': 'uint32', '17': 'sint32'}
names = {}


def uname(key):
    if key in names:
        return names[key]
    base = msgs[key]['spec'].split('|')[0]
    n = base
    i = 2
    while n in names.values():
        n = '%s~%d' % (base, i)
        i += 1
    names[key] = n
    return n


def parse(key):
    m = msgs[key]
    fields = []
    for p in m['spec'].split('|')[1:]:
        t = p.split(' ')
        typ = t[2] if len(t) > 2 else ''
        f = {'num': int(t[0]), 'name': t[1], 'oneof': t[3] if len(t) > 3 else ''}
        f['rep'] = typ.endswith('*')
        typ = typ.rstrip('?*')
        if ',' in typ:
            k, v = typ.split(',')
            kv = [('k', k), ('v', v)]
            f['map'] = True
        else:
            kv = [('t', typ)]
        for slot, tv in kv:
            if tv.startswith('#'):
                ref = m['refs'][int(tv[1:])]
                r = resolve(m['module'], ref)
                f[slot] = r if r else ('unresolved', ref)
            else:
                f[slot] = ('s', SCALAR.get(tv, 'unknown:' + tv))
        fields.append(f)
    return fields


def find(prefix):
    return [k for k, m in msgs.items() if m['spec'].startswith(prefix)]


roots = find('ToolCall|1 shell_tool_call')[:1]
for pre in ['ConversationStateStructure|', 'ConversationStep|', 'ConversationTurnStructure|',
            'AgentConversationTurnStructure|', 'ShellConversationTurnStructure|', 'UserMessage|',
            'ConversationSummary|', 'ConversationSummaryArchive|', 'TodoItem|', 'SelectedContext|',
            'SubagentPersistedState|', 'AssistantMessage|', 'ThinkingMessage|']:
    roots += find(pre)
seen = set()
order = []
stack = list(roots)
unresolved = []
parsed = {}
while stack:
    k = stack.pop()
    if k in seen:
        continue
    seen.add(k)
    order.append(k)
    parsed[k] = parse(k)
    for f in parsed[k]:
        for slot in ('t', 'k', 'v'):
            if slot in f and f[slot][0] == 'm':
                stack.append(f[slot][1])
            if slot in f and f[slot][0] == 'unresolved':
                unresolved.append((msgs[k]['spec'].split('|')[0], f['name'], f[slot][1]))
for k in sorted(order, key=lambda k: (msgs[k]['spec'].split('|')[0], k)):
    uname(k)


def ts(t):
    if t[0] == 's':
        return t[1]
    if t[0] == 'm':
        return 'M:' + names[t[1]]
    if t[0] == 'e':
        return 'E:' + enums[t[1]][0]
    return 'M:google.protobuf.Value'


print('// Code generated by scripts/cursor_agent_schema.py from the official Cursor Agent CLI bundle; DO NOT EDIT.')
print('// Source bundle: ' + os.path.basename(os.path.normpath(root)))
print('package drain\n')
print('// Field spec: number:name:type[:oneof]; type is a protobuf scalar, M:Message, E:Enum, trailing * = repeated, map<k,v>.')
print('var cursorAgentSchema = map[string]string{')
used_enums = {}
for k in sorted(order, key=lambda k: names[k]):
    items = []
    for f in parsed[k]:
        if 'map' in f:
            typ = 'map<%s,%s>' % (ts(f['k']), ts(f['v']))
        else:
            typ = ts(f['t'])
        for slot in ('t', 'k', 'v'):
            if slot in f and f[slot][0] == 'e':
                used_enums[f[slot][1]] = 1
        if f['rep']:
            typ += '*'
        item = '%d:%s:%s' % (f['num'], f['name'], typ)
        if f['oneof']:
            item += ':' + f['oneof']
        items.append(item)
    print('\t"%s": "%s",' % (names[k], ' '.join(items)))
print('}\n')
print('var cursorAgentEnums = map[string]string{')
for k in sorted(used_enums, key=lambda k: enums[k][0]):
    n, vals = enums[k]
    print('\t"%s": "%s",' % (n, ' '.join('%d=%s' % v for v in vals)))
print('}')
sys.stderr.write('messages %d enums %d unresolved %d\n' % (len(order), len(used_enums), len(unresolved)))
for u in unresolved[:30]:
    sys.stderr.write('unresolved %r\n' % (u,))

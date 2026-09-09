#!/usr/bin/env sh
# Measure prompt-guard against public prompt-injection datasets.
#
# These datasets are NOT committed: they carry their own licences and change
# upstream. This script fetches them from the Hugging Face datasets-server,
# converts them to the corpus format and runs `eval`. Run it from the repo
# root; it needs network access.
#
#   sh plugins/internal/cmd/prompt-guard-corpus/external-benchmark.sh
#
# What the numbers mean: these sets are the real-world check the synthetic
# corpus cannot be. High precision with modest recall is expected and is the
# reason prompt-guard ships blocking-off by default. Note that a few English
# rules were tuned after reading deepset's misses, so deepset recall is
# optimistic; jackhhao/jailbreak-classification was only ever measured in
# aggregate and stays the cleaner held-out figure.
set -e
DIR="${TMPDIR:-/tmp}/prompt-guard-bench"
mkdir -p "$DIR"
BIN="$DIR/pgc"
go build -o "$BIN" ./plugins/internal/cmd/prompt-guard-corpus

fetch() { # dataset split textcol python-malicious-expr outfile
  ds="$1"; sp="$2"; tcol="$3"; mal="$4"; out="$5"; : > "$out"; off=0
  while :; do
    enc=$(python3 -c "import urllib.parse,sys;print(urllib.parse.quote(sys.argv[1]))" "$ds")
    url="https://datasets-server.huggingface.co/rows?dataset=$enc&config=default&split=$sp&offset=$off&length=100"
    curl -sS -m 60 "$url" > "$DIR/page.json"
    n=$(python3 -c "import json;print(len(json.load(open('$DIR/page.json')).get('rows',[])))")
    [ "$n" = "0" ] && break
    python3 - "$out" "$tcol" "$mal" <<PY
import json,sys
out,tcol,mal=sys.argv[1],sys.argv[2],sys.argv[3]
rows=[r['row'] for r in json.load(open("$DIR/page.json"))['rows']]
with open(out,'a') as f:
    for r in rows:
        t=(r.get(tcol) or '').strip()
        if not t: continue
        m=bool(eval(mal,{'r':r}))
        f.write(json.dumps({'text':t,'malicious':m,'family':'ext','split':'test','language':'en','source':'user','origin':'external'},ensure_ascii=False)+"\n")
PY
    off=$((off+100)); [ "$n" -lt 100 ] && break
  done
  echo "$ds/$sp -> $(wc -l < "$out") rows"
}

fetch "deepset/prompt-injections"        test  text   'int(r.get("label",0))==1'      "$DIR/deepset.jsonl"
fetch "jackhhao/jailbreak-classification" test  prompt 'r.get("type")=="jailbreak"'    "$DIR/jailbreak.jsonl"

for d in deepset jailbreak; do
  echo "════ $d"
  "$BIN" eval -corpus "$DIR/$d.jsonl" -show-fp 0 -show-fn 0 | grep '^all '
done

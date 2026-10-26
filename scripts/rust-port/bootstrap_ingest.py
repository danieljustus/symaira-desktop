"""One-time mechanical extraction of SQL; review resulting Rust source."""
from pathlib import Path
import re
root = Path(__file__).resolve().parents[2]
crate = root / 'crates/symdesk-ingest'
(crate/'src').mkdir(parents=True, exist_ok=True)
(crate/'migrations').mkdir(exist_ok=True)
source = (root/'internal/ingest/internal/store/store.go').read_text()
constants=[]
for name in ['EnqueueJob','EnqueueSkippedJob','EnqueueReprocessJob','ClaimJobByID','ClaimJob','CompleteJob','FailJob','RetryJob','ResetRunningJobs']:
    part=source.split('func (s *Store) '+name+'(')[1].split('\nfunc ')[0]
    for i,sql in enumerate(re.findall(r'`([^`]*)`',part)):
        constants.append(f'pub(crate) const {re.sub(r"(?<!^)(?=[A-Z])", "_", name).upper()}_{i}: &str = r#"{sql}"#;\n')
(crate/'src/sql.rs').write_text('// SQL copied verbatim from Go store.go.\n'+''.join(constants))
migrations=[]
for p in sorted((root/'internal/ingest/internal/store/migrations').glob('*.sql')):
    (crate/'migrations'/p.name).write_bytes(p.read_bytes())
    migrations.append(f'    ("{p.stem}", include_str!("../migrations/{p.name}")),')
(crate/'src/migrations.rs').write_text('pub const MIGRATIONS: &[(&str, &str)] = &[\n'+'\n'.join(migrations)+'\n];\n')

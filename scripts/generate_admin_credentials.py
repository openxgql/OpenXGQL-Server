#!/usr/bin/env python3
"""Generate credentials once; never print secret values."""
import hashlib,os,secrets,string,sys
from pathlib import Path
credential=Path(sys.argv[1]);envfile=Path(sys.argv[2])
if credential.exists() and envfile.exists():
    print('Existing admin credentials retained')
    sys.exit(0)
letters=string.ascii_letters;digits=string.digits;symbols='!@#$%^&*()-_=+[]{}:,.?/'
chars=[secrets.choice(letters),secrets.choice(digits),secrets.choice(symbols)]
chars += [secrets.choice(letters+digits+symbols) for _ in range(97)]
secrets.SystemRandom().shuffle(chars);password=''.join(chars)
base=os.environ.get('PUBLIC_BASE_URL','https://flashlarry.duckdns.org').rstrip('/')
for p,text in [(credential,'URL: '+base+'/admin/\nUsername: admin\nPassword: '+password+'\n'),(envfile,'ADMIN_USERNAME=admin\nADMIN_PASSWORD_SHA256='+hashlib.sha256(password.encode()).hexdigest()+'\n')]:
    p.parent.mkdir(parents=True,exist_ok=True)
    fd=os.open(p,os.O_WRONLY|os.O_CREAT|os.O_EXCL,0o600)
    with os.fdopen(fd,'w') as f:f.write(text)
print('Admin password generated: 100 characters, letters, digits and symbols')

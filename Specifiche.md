# Bashtransfer

## Idea

Un piccolo servizio, da deployare su una VM remota, che mi consente di trasferire/scaricare tramite commnadline file e cartelle.

Esempi di funzionamento

## scenario 1

- server attivo su https://bashtransfer.mydomain.it
- il server non deve mantenere file, ma fare solo da relay

- lato chi invia: curl -T myfile.zip https://basht.mydomain.it
    - questo genera un comando:
        - curl -o "myfile.zip" "https://basht.mydomain.it/<10-char-slug>"
        - wget .... (comando equivalente wget)
    - si mette in attesa che qualcuno scarichi

- lato ricevente: curl -o "myfile.zip" "https://basht.mydomain.it/<10-char-slug>"
    - parte il trasferimento del file

## scenario 2

- lato chi invia: sudo tar -cpzf - mydir | curl -T - https://basht.mydomain.it/mydir.tgz
    - questo genera un comando:
        - curl -o "myfile.zip" "https://basht.mydomain.it/<10-char-slug>"
        - wget .... (comando equivalente wget)
    - si mette in attesa che qualcuno scarichi
    - il tar deve attedere che qualcuno scarichi, deve essere streaming, non salvare prima il tar su disco!

- lato ricevente: curl -o "myfile.zip" "https://basht.mydomain.it/<10-char-slug>"
    - parte il trasferimento del file


## Specifiche

- Opzionale basic auth con username:password (default servizio pubblico)
- Performance elevate di trasferimento (rust/go), deci tu il migliore, voglio sfruttare tutta la banda che ho a disposizione
- Non gestiamo certificati, lo metteremo dietro nginx
- Deploy con docker
- Compose nginx + app
- Documentazione + Test

# MySQL example

Needs a local MySQL/MariaDB with a `myapp` database and the `mysql`/`mysqldump` client tools.

    mysql -uroot -proot -e 'CREATE DATABASE myapp'
    agentbox new a && agentbox new b
    agentbox ls        # databases myapp_agentbox_a and myapp_agentbox_b, each a fresh dump of myapp

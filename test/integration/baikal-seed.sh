#!/bin/sh
# Replaces Baïkal's install wizard: writes its config and creates one user
# with one calendar. Test only.
set -e

cat > /var/www/baikal/config/baikal.yaml <<'YAML'
system:
  configured_version: "0.10.1"
  timezone: Europe/Amsterdam
  card_enabled: true
  cal_enabled: true
  invite_from: ""
  dav_auth_type: Basic
  admin_passwordhash: unused-the-admin-ui-is-not-tested
  failed_access_message: ""
  auth_realm: BaikalDAV
  base_uri: ""
database:
  sqlite_file: /var/www/baikal/Specific/db/db.sqlite
  backend: sqlite
  encryption_key: test-encryption-key
YAML

php <<'PHP'
<?php
$db = new PDO('sqlite:/var/www/baikal/Specific/db/db.sqlite');
$db->setAttribute(PDO::ATTR_ERRMODE, PDO::ERRMODE_EXCEPTION);
if ($db->query("SELECT 1 FROM sqlite_master WHERE name = 'users'")->fetch()) {
    exit(0);
}
$db->exec(file_get_contents('/var/www/baikal/Core/Resources/Db/SQLite/db.sql'));
$digest = md5('tester:BaikalDAV:test-password');
$db->exec("INSERT INTO users (username, digesta1) VALUES ('tester', '$digest')");
$db->exec("INSERT INTO principals (uri, email, displayname) VALUES ('principals/tester', 'tester@example.org', 'Tester')");
$db->exec("INSERT INTO calendars (synctoken, components) VALUES (1, 'VEVENT')");
$id = $db->lastInsertId();
$db->exec("INSERT INTO calendarinstances (calendarid, principaluri, access, displayname, uri, transparent)
           VALUES ($id, 'principals/tester', 1, 'Default', 'default', 0)");
PHP

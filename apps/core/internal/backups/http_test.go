package backups

import (
	"encoding/json"
	"fmt"
	"testing"

	"homelab-ngx.com/core/backup-runner/runfile"
)

func body(t *testing.T, v any) string {
	t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

func TestHTTPBackups(t *testing.T) {
	t.Parallel()
	h := newHarness(t)
	h.setState(succeed)

	var created DestinationCreated
	if code, m := call(t, h, "POST", "/api/backup-destinations", body(t, resticInput("Repo", "/mnt/usb/repo", "")), &created); code != 201 {
		t.Fatalf("create destination = %d %v", code, m)
	}
	if created.GeneratedPassword == "" || created.Destination.PasswordConfirmed {
		t.Fatalf("created = %+v", created)
	}
	dest := fmt.Sprintf("/api/backup-destinations/%d", created.Destination.Id)

	var b Backup
	in := fmt.Sprintf(`{"name":"Nightly","schedule":"0 4 * * *","destination_ids":[%d],"sources":[{"kind":"path","path":%q}]}`,
		created.Destination.Id, h.data)
	if code, m := call(t, h, "POST", "/api/backups", in, &b); code != 201 || b.Id == 0 || len(b.Sources) != 1 {
		t.Fatalf("create backup = %d %v", code, m)
	}
	one := fmt.Sprintf("/api/backups/%d", b.Id)

	expect := func(method, path, reqBody string, status int, code string) {
		t.Helper()
		got, m := call(t, h, method, path, reqBody, nil)
		if got != status || (code != "" && m["code"] != code) {
			t.Errorf("%s %s = %d %v, want %d %s", method, path, got, m, status, code)
		}
	}
	expect("POST", "/api/backups", in, 409, "name_taken")
	expect("POST", "/api/backups", `{"name":"x","schedule":"nope"}`, 400, "invalid_schedule")
	expect("POST", "/api/backups", `{"name":"x","sources":[{"kind":"path","path":"relative"}]}`, 400, "invalid_path")
	expect("POST", "/api/backups", `{"name":"x","destination_ids":[999]}`, 400, "invalid_destination")
	expect("GET", "/api/backups/999", "", 404, "unknown_backup")
	expect("PATCH", "/api/backups/999", `{}`, 404, "unknown_backup")
	expect("PATCH", one, `{"bwlimit":"fast"}`, 400, "invalid_bwlimit")
	expect("DELETE", "/api/backups/999", "", 404, "unknown_backup")
	expect("POST", "/api/backups/999/run", "", 404, "unknown_backup")
	expect("GET", "/api/backups/999/runs", "", 404, "unknown_backup")
	expect("GET", "/api/backups/999/logs", "", 404, "unknown_backup")
	expect("GET", "/api/backup-runs/999", "", 404, "unknown_run")
	// The password is not confirmed yet.
	expect("POST", one+"/run", "", 409, "not_ready")

	expect("POST", "/api/backup-destinations", body(t, resticInput("Repo", "/x", "")), 409, "name_taken")
	expect("POST", "/api/backup-destinations", `{"name":"x","kind":"s3","s3":{"endpoint":"nope","bucket":"b","access_key_id":"a","secret_access_key":"s"}}`, 400, "invalid_endpoint")
	expect("PUT", "/api/backup-destinations/999", body(t, s3Input("x")), 404, "unknown_destination")
	expect("DELETE", dest, "", 409, "destination_in_use")
	expect("DELETE", "/api/backup-destinations/999", "", 404, "unknown_destination")
	expect("POST", "/api/backup-destinations/999/test", "", 404, "unknown_destination")
	expect("POST", dest+"/reveal-password", `{"confirm":"wrong"}`, 400, "confirm_mismatch")
	var pw DestinationPassword
	if code, _ := call(t, h, "POST", dest+"/reveal-password", `{"confirm":"Repo"}`, &pw); code != 200 || pw.Password != created.GeneratedPassword {
		t.Errorf("reveal = %d %+v", code, pw)
	}
	var d Destination
	if code, _ := call(t, h, "POST", dest+"/confirm-password", "", &d); code != 200 || !d.PasswordConfirmed {
		t.Errorf("confirm = %d %+v", code, d)
	}

	var started BackupRunStarted
	if code, m := call(t, h, "POST", one+"/run", "", &started); code != 202 || started.RunId == 0 {
		t.Fatalf("run = %d %v", code, m)
	}
	h.svc.Wait()
	var detail BackupRunDetail
	if code, _ := call(t, h, "GET", fmt.Sprintf("/api/backup-runs/%d", started.RunId), "", &detail); code != 200 ||
		detail.Result != BackupRunResultSuccess || len(detail.Items) != 2 || detail.Items[1].Kind != runfile.ItemFolder {
		t.Fatalf("run detail = %d %+v", code, detail)
	}
	var runs []BackupRun
	if code, _ := call(t, h, "GET", one+"/runs?limit=5", "", &runs); code != 200 || len(runs) != 1 || runs[0].Id != started.RunId {
		t.Errorf("runs = %d %+v", code, runs)
	}
	expect("GET", one+"/runs?limit=0", "", 422, "")
	var list []Backup
	if code, _ := call(t, h, "GET", "/api/backups", "", &list); code != 200 || len(list) != 1 || list[0].LastRun == nil {
		t.Errorf("list = %d %+v", code, list)
	}
	var cov Coverage
	if code, _ := call(t, h, "GET", "/api/backup-coverage", "", &cov); code != 200 || cov.Stacks == nil {
		t.Errorf("coverage = %d %+v", code, cov)
	}
	var patched Backup
	if code, _ := call(t, h, "PATCH", one, `{"enabled":false,"destination_ids":[]}`, &patched); code != 200 || patched.Enabled ||
		len(patched.Destinations) != 0 || len(patched.Sources) != 1 {
		t.Errorf("patch = %d %+v", code, patched)
	}
	expect("DELETE", dest, "", 204, "")
	expect("DELETE", one, "", 204, "")
	expect("GET", one, "", 404, "unknown_backup")
}

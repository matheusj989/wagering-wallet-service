# Smoke test do SQS do MiniStack (validação de 20/09/2026).
# Uso: docker run -d --name ms -p 127.0.0.1:4566:4566 ministackorg/ministack:1.5.14
#      python3 ministack_probe.py http://127.0.0.1:4566
# Observação: o último cenário ("uma por grupo por vez") tinha expectativa errada: SQS real e MiniStack
# entregam várias mensagens do MESMO grupo num único ReceiveMessage com MaxNumberOfMessages > 1.
# Isso motivou a decisão de consumir com MaxNumberOfMessages = 1 (design.md).
import json, re, sys, time, urllib.request, urllib.error
BASE = sys.argv[1]
def sqs(target, payload):
    req = urllib.request.Request(BASE + '/', data=json.dumps(payload).encode(), method='POST', headers={
        'Content-Type': 'application/x-amz-json-1.0', 'X-Amz-Target': 'AmazonSQS.' + target,
        'Authorization': 'AWS4-HMAC-SHA256 Credential=test/20260920/us-east-1/sqs/aws4_request, SignedHeaders=host;x-amz-date, Signature=deadbeef',
        'X-Amz-Date': '20260920T000000Z'})
    try:
        with urllib.request.urlopen(req, timeout=20) as r: body = r.read().decode()
    except urllib.error.HTTPError as e:
        print(f"  !! {target} HTTP {e.code}: {e.read().decode()[:300]}"); return {}
    return json.loads(body) if body.strip() else {}
def fix(u): return re.sub(r'^https?://[^/]+', BASE, u)
def ok(cond, label): print(("PASS " if cond else "FAIL ") + label)
def recv(url, n=1, vis=None, wait=0):
    p = {"QueueUrl": url, "MaxNumberOfMessages": n, "WaitTimeSeconds": wait, "AttributeNames": ["ApproximateReceiveCount", "MessageGroupId"], "MessageSystemAttributeNames": ["ApproximateReceiveCount", "MessageGroupId"]}
    if vis is not None: p["VisibilityTimeout"] = vis
    return sqs("ReceiveMessage", p).get("Messages", [])

dlq = fix(sqs("CreateQueue", {"QueueName": "probe-dlq.fifo", "Attributes": {"FifoQueue": "true"}})["QueueUrl"])
dlq_arn = sqs("GetQueueAttributes", {"QueueUrl": dlq, "AttributeNames": ["QueueArn"]})["Attributes"]["QueueArn"]
ok(dlq.endswith(".fifo") and ":probe-dlq.fifo" in dlq_arn, f"cria FIFO + DLQ, arn={dlq_arn}")
main = fix(sqs("CreateQueue", {"QueueName": "probe.fifo", "Attributes": {"FifoQueue": "true", "VisibilityTimeout": "1",
        "RedrivePolicy": json.dumps({"deadLetterTargetArn": dlq_arn, "maxReceiveCount": "2"})}})["QueueUrl"])
attrs = sqs("GetQueueAttributes", {"QueueUrl": main, "AttributeNames": ["All"]})["Attributes"]
ok("RedrivePolicy" in attrs and attrs.get("FifoQueue") == "true", f"RedrivePolicy aceito e persistido: {attrs.get('RedrivePolicy')}")

sqs("SendMessage", {"QueueUrl": main, "MessageBody": "m1", "MessageGroupId": "wallet-A", "MessageDeduplicationId": "m1"})
r1 = recv(main, vis=1)
ok(len(r1) == 1 and r1[0]["Body"] == "m1", "1o receive entrega m1")
c1 = r1[0].get("Attributes", {}).get("ApproximateReceiveCount") if r1 else None
ok(c1 == "1", f"ApproximateReceiveCount no 1o receive = {c1}")
ok(recv(main) == [], "m1 invisivel enquanto dentro do visibility timeout")
time.sleep(1.5)
r2 = recv(main, vis=1)
c2 = r2[0].get("Attributes", {}).get("ApproximateReceiveCount") if r2 else None
ok(len(r2) == 1 and c2 == "2", f"apos visibility timeout m1 volta, ApproximateReceiveCount = {c2}")
time.sleep(1.5)
r3 = recv(main, vis=1)
d = recv(dlq)
ok(r3 == [] and len(d) == 1 and d[0]["Body"] == "m1", f"3o receive com maxReceiveCount=2: fila principal vazia={r3==[]}, na DLQ={[m['Body'] for m in d]}")

sqs("SendMessage", {"QueueUrl": main, "MessageBody": "m2", "MessageGroupId": "wallet-B", "MessageDeduplicationId": "m2"})
r = recv(main, vis=30)
ok(len(r) == 1 and recv(main) == [], "m2 recebido com visibility 30s e invisivel em seguida")
sqs("ChangeMessageVisibility", {"QueueUrl": main, "ReceiptHandle": r[0]["ReceiptHandle"], "VisibilityTimeout": 0})
r = recv(main, vis=30)
ok(len(r) == 1 and r[0]["Body"] == "m2", "ChangeMessageVisibility(0) reentrega m2 imediatamente")
sqs("DeleteMessage", {"QueueUrl": main, "ReceiptHandle": r[0]["ReceiptHandle"]})
ok(recv(main) == [], "DeleteMessage remove m2")

for i in (3, 4, 5):
    sqs("SendMessage", {"QueueUrl": main, "MessageBody": f"m{i}", "MessageGroupId": "wallet-C", "MessageDeduplicationId": f"m{i}"})
sqs("SendMessage", {"QueueUrl": main, "MessageBody": "m6", "MessageGroupId": "wallet-D", "MessageDeduplicationId": "m6"})
a = recv(main, n=1, vis=30)
b = recv(main, n=10, vis=30)
ok(len(a) == 1 and a[0]["Body"] == "m3", f"primeiro do grupo C = {a[0]['Body'] if a else None}")
ok([m["Body"] for m in b] == ["m6"], f"com m3 em voo, so outro grupo e entregue: {[m['Body'] for m in b]}")
sqs("DeleteMessage", {"QueueUrl": main, "ReceiptHandle": a[0]["ReceiptHandle"]})
c = recv(main, n=10, vis=30)
ok([m["Body"] for m in c] == ["m4", "m5"], f"apos apagar m3, receive com n=10 devolve o resto do grupo C em ordem: {[m['Body'] for m in c]}")

sqs("SendMessage", {"QueueUrl": main, "MessageBody": "dup", "MessageGroupId": "wallet-E", "MessageDeduplicationId": "dup-1"})
sqs("SendMessage", {"QueueUrl": main, "MessageBody": "dup", "MessageGroupId": "wallet-E", "MessageDeduplicationId": "dup-1"})
e = recv(main, n=10, vis=30)
ok(len([m for m in e if m["Body"] == "dup"]) == 1, f"MessageDeduplicationId repetido entrega 1 msg: {[m['Body'] for m in e]}")

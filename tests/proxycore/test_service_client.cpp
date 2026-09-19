// REC-03: targeted tests for the service pipe client (API::ServiceClient)
// against an in-process fake pipe server speaking the same PC-110 envelope
// protocol. Covered: framing (partial delivery, oversized declaration),
// request/response id matching (including the late-response drop), envelope
// codes vs typed payload errors, connection break (unknown Start outcome +
// reconnect with a fresh Hello), version mismatch, absence of the service,
// and pipe access denial (Windows: a raw DACL-restricted pipe).
//
// The client's pipe name is the fixed production pipe, so the fake server
// binds \\.\pipe\ProxyCoreService: run this harness WITHOUT the real
// ProxyCoreService running (the pipe must be free; the VM smoke runs
// separately).
//
// Layout: the Qt event loop lives on the main thread; test cases run on a
// worker std::thread (client calls block their caller only); each fake
// server runs on its own QThread with a running loop, so socket events are
// always delivered while the worker is blocked inside a client call. A
// settle() pause between cases lets the client's io thread process the
// server teardown before the next case asserts on a fresh connect.

#include <QCoreApplication>
#include <QLocalServer>
#include <QLocalSocket>
#include <QObject>
#include <QThread>
#include <QTimer>
#include <QtEndian>

#include <core/server/gen/libcore.pb.h>

#include "include/api/ServiceClient.h"

#include <atomic>
#include <chrono>
#include <cstdio>
#include <cstring>
#include <functional>
#include <memory>
#include <mutex>
#include <string>
#include <thread>
#include <vector>

#ifdef Q_OS_WIN
#include <windows.h>
#include <sddl.h>
#endif

#define CHECK(cond)                                                                       \
    do {                                                                                  \
        if (!(cond)) {                                                                    \
            fprintf(stderr, "CHECK failed at %s:%d: %s\n", __FILE__, __LINE__, #cond);    \
            fflush(stderr);                                                               \
            failures++;                                                                   \
        }                                                                                 \
    } while (false)

namespace {

    using Handler = std::function<void(class ServerConn &conn, const libcore::RequestEnvelope &env)>;

    void settle() { std::this_thread::sleep_for(std::chrono::milliseconds(150)); }

    QByteArray frameMessage(const libcore::ResponseEnvelope &r) {
        const std::string body = spb::pb::serialize<std::string>(r);
        QByteArray f;
        f.resize(qint64(4 + body.size()));
        qToLittleEndian<quint32>(quint32(body.size()), f.data());
        std::memcpy(f.data() + 4, body.data(), body.size());
        return f;
    }

    template <typename T>
    std::vector<std::byte> payloadOf(const T &msg) {
        const std::string s = spb::pb::serialize<std::string>(msg);
        const auto *from = reinterpret_cast<const std::byte *>(s.data());
        return {from, from + s.size()};
    }

    libcore::ResponseEnvelope okEnvelope(const libcore::RequestEnvelope &req, std::vector<std::byte> payload) {
        libcore::ResponseEnvelope r;
        r.request_id = req.request_id.value_or(0);
        r.code = 0;
        r.service_protocol_version = API::ServiceClient::kProtocolVersion;
        if (!payload.empty()) r.typed_payload = std::move(payload);
        return r;
    }

    libcore::ResponseEnvelope errorEnvelope(const libcore::RequestEnvelope &req, int code, const char *message) {
        libcore::ResponseEnvelope r;
        r.request_id = req.request_id.value_or(0);
        r.code = code;
        r.message = message;
        r.service_protocol_version = API::ServiceClient::kProtocolVersion;
        return r;
    }

    // Per-connection state; lives and runs on the server thread only.
    class ServerConn {
    public:
        QLocalSocket *sock = nullptr;
        QByteArray buf;
        bool handshaken = false;
    };

    // One fake service on its own QThread, bound to the fixed production
    // pipe name. `handler` runs on the server thread for every decoded
    // request; `stats` records cross-case statistics (thread-safe).
    class ServerThread {
    public:
        struct Stats {
            std::mutex mu;
            std::vector<libcore::RequestEnvelope> seen;
            int connections = 0;
            bool handshakeViolation = false;
        };

        ServerThread(Handler handler, std::shared_ptr<Stats> sharedStats)
            : handler(std::move(handler)), stats(std::move(sharedStats)) {
            thread = new QThread;
            anchor = new QObject;
            anchor->moveToThread(thread);
            thread->start();
            QMetaObject::invokeMethod(
                anchor, [this] { setup(); }, Qt::BlockingQueuedConnection);
        }

        ~ServerThread() {
            QMetaObject::invokeMethod(
                anchor, [this] { teardown(); }, Qt::BlockingQueuedConnection);
            thread->quit();
            thread->wait();
            delete anchor;
            delete thread;
        }

        // Accessor from the test (worker) thread.
        int connections() {
            std::lock_guard<std::mutex> lock(stats->mu);
            return stats->connections;
        }

    private:
        void setup() {
            server = new QLocalServer(anchor);
            if (!server->listen(QStringLiteral("ProxyCoreService"))) {
                qWarning("fake service: cannot bind the pipe: %s", qUtf8Printable(server->errorString()));
                return;
            }
            QObject::connect(server, &QLocalServer::newConnection, anchor, [this] { accept(); });
        }

        void teardown() {
            for (auto &c : conns) {
                if (c->sock != nullptr) c->sock->close();
            }
            conns.clear();
            if (server != nullptr) {
                server->close();
                server->deleteLater();
                server = nullptr;
            }
        }

        void accept() {
            while (server != nullptr) {
                QLocalSocket *s = server->nextPendingConnection();
                if (s == nullptr) break;
                {
                    std::lock_guard<std::mutex> lock(stats->mu);
                    stats->connections++;
                }
                conns.push_back(std::make_unique<ServerConn>());
                auto *c = conns.back().get();
                c->sock = s;
                QObject::connect(s, &QLocalSocket::readyRead, anchor, [this, c] { read(c); });
                QObject::connect(s, &QLocalSocket::disconnected, anchor, [this, c] { c->sock = nullptr; });
            }
        }

        void read(ServerConn *c) {
            if (c->sock == nullptr) return;
            c->buf += c->sock->readAll();
            while (true) {
                if (c->buf.size() < 4) return;
                const quint32 len = qFromLittleEndian<quint32>(c->buf.constData());
                if (c->buf.size() < qint64(4 + len)) return;
                libcore::RequestEnvelope env;
                bool parsed = true;
                try {
                    env = spb::pb::deserialize<libcore::RequestEnvelope>(std::vector<std::uint8_t>(
                        reinterpret_cast<const std::uint8_t *>(c->buf.constData() + 4),
                        reinterpret_cast<const std::uint8_t *>(c->buf.constData() + 4 + len)));
                } catch (...) {
                    parsed = false;
                }
                c->buf.remove(0, qint64(4 + len));
                if (!parsed) return;

                {
                    std::lock_guard<std::mutex> lock(stats->mu);
                    stats->seen.push_back(env);
                    if (!c->handshaken && env.operation.value_or("") != "Hello") {
                        stats->handshakeViolation = true;
                    }
                    c->handshaken = true;
                }
                try {
                    handler(*c, env);
                } catch (...) {
                    fprintf(stderr, "handler threw (server thread)
");
                    fflush(stderr);
                    failures++;
                }
            }
        }

    public:
        // Server-thread helpers used by handlers.
        static void send(ServerConn *c, const libcore::ResponseEnvelope &r) {
            if (c->sock != nullptr) {
                c->sock->write(frameMessage(r));
                c->sock->flush();
            }
        }

        static void sendRaw(ServerConn *c, const QByteArray &bytes) {
            if (c->sock != nullptr) {
                c->sock->write(bytes);
                c->sock->flush();
            }
        }

        // Deliver one response in three write() calls with pauses in between:
        // the client must reassemble a frame arriving in fragments.
        static void sendPartial(ServerConn *c, const libcore::ResponseEnvelope &r) {
            if (c->sock == nullptr) return;
            const QByteArray frame = frameMessage(r);
            const QByteArray first = frame.left(3);
            const QByteArray second = frame.mid(3, 5);
            const QByteArray rest = frame.mid(8);
            QLocalSocket *sock = c->sock;
            sock->write(first);
            sock->flush();
            QTimer::singleShot(20, sock, [sock, second, rest] {
                sock->write(second);
                sock->flush();
                QTimer::singleShot(20, sock, [sock, rest] {
                    sock->write(rest);
                    sock->flush();
                });
            });
        }

    private:
        QThread *thread = nullptr;
        QObject *anchor = nullptr;
        QLocalServer *server = nullptr;
        std::vector<std::unique_ptr<ServerConn>> conns;
        Handler handler;
        std::shared_ptr<Stats> stats;
    };

    int failures = 0;

    using Stats = ServerThread::Stats;

    // Standard scenario branches shared by several handlers: the handshake
    // answer and a truthful Health answer.
    void replyHello(ServerConn &c, const libcore::RequestEnvelope &env, const char *version = "fake-1") {
        libcore::HandshakeResp h;
        h.protocol_version = API::ServiceClient::kProtocolVersion;
        h.service_version = version;
        ServerThread::send(&c, okEnvelope(env, payloadOf(h)));
    }

    void replyHealth(ServerConn &c, const libcore::RequestEnvelope &env, bool running) {
        libcore::HealthResp h;
        h.runtime_running = running;
        h.protocol_version = API::ServiceClient::kProtocolVersion;
        ServerThread::send(&c, okEnvelope(env, payloadOf(h)));
    }

    void assertEnvelopeDiscipline(const std::shared_ptr<Stats> &stats) {
        std::lock_guard<std::mutex> lock(stats->mu);
        CHECK(!stats->handshakeViolation);
        std::uint64_t prev = 0;
        for (const auto &env : stats->seen) {
            CHECK(env.request_id.value_or(0) != 0);
            CHECK(env.protocol_version.value_or(0) == API::ServiceClient::kProtocolVersion);
            CHECK(env.expected_policy_revision.value_or(0) == API::ServiceClient::kConfigPolicyRevision);
            CHECK(env.request_id.value_or(0) > prev); // never reused, strictly increasing
            prev = env.request_id.value_or(0);
        }
    }

    void run_all() {
        API::ServiceClient client;

        // ---- T1: happy Hello + Health + CheckConfig + Start + Stop --------
        try {
            auto stats = std::make_shared<Stats>();
            ServerThread svc(
                [](ServerConn &c, const libcore::RequestEnvelope &env) {
                    const QString op = QString::fromStdString(env.operation.value_or(""));
                    if (op == "Hello") {
                        replyHello(c, env);
                    } else if (op == "Health") {
                        replyHealth(c, env, true);
                    } else {
                        libcore::ErrorResp e;
                        ServerThread::send(&c, okEnvelope(env, payloadOf(e)));
                    }
                },
                stats);

            API::ServiceClient::HelloInfo hello;
            auto r = client.Hello(&hello, 5000);
            CHECK(r.ok());
            CHECK(hello.protocolVersion == API::ServiceClient::kProtocolVersion);
            CHECK(hello.serviceVersion == "fake-1");

            API::ServiceClient::HealthInfo health;
            r = client.Health(&health, 5000);
            CHECK(r.ok());
            CHECK(health.runtimeRunning);

            libcore::LoadConfigReq req;
            req.core_config = std::string(R"({"log":{"level":"warn"},"inbounds":[],"outbounds":[]})");
            r = client.CheckConfig(req, 5000);
            CHECK(r.ok());
            CHECK(r.outcomeKnown());

            r = client.Start(req, 5000);
            CHECK(r.ok());

            r = client.Stop(5000);
            CHECK(r.ok());
            assertEnvelopeDiscipline(stats);
        }
        catch (const std::exception &e) {
            fprintf(stderr, "case threw (near line %d): %s
", __LINE__, e.what());
            fflush(stderr);
            failures++;
        }
        catch (...) {
            fprintf(stderr, "case threw (unknown, near line %d)
", __LINE__);
            fflush(stderr);
            failures++;
        }

        // ---- T2: partial response delivery (framing) ----------------------
        try {
            settle();
            auto stats = std::make_shared<Stats>();
            ServerThread svc(
                [](ServerConn &c, const libcore::RequestEnvelope &env) {
                    if (env.operation.value_or("") == "Hello") {
                        replyHello(c, env);
                    } else if (env.operation.value_or("") == "Health") {
                        libcore::HealthResp h;
                        h.runtime_running = false;
                        ServerThread::sendPartial(&c, okEnvelope(env, payloadOf(h)));
                    }
                },
                stats);
            API::ServiceClient::HealthInfo health;
            const auto r = client.Health(&health, 5000);
            CHECK(r.ok());
            CHECK(!health.runtimeRunning);
            assertEnvelopeDiscipline(stats);
        }
        catch (const std::exception &e) {
            fprintf(stderr, "case threw (near line %d): %s
", __LINE__, e.what());
            fflush(stderr);
            failures++;
        }
        catch (...) {
            fprintf(stderr, "case threw (unknown, near line %d)
", __LINE__);
            fflush(stderr);
            failures++;
        }

        // ---- T3: envelope code (stale policy revision) ---------------------
        try {
            settle();
            auto stats = std::make_shared<Stats>();
            ServerThread svc(
                [](ServerConn &c, const libcore::RequestEnvelope &env) {
                    if (env.operation.value_or("") == "Hello") {
                        replyHello(c, env);
                        return;
                    }
                    ServerThread::send(&c, errorEnvelope(env, 6, "ERR_STALE_POLICY_REVISION: client expects 1, service 2"));
                },
                stats);
            const auto r = client.CheckConfig({}, 5000);
            CHECK(r.outcome == API::ServiceClient::Outcome::EnvelopeError);
            CHECK(r.envelopeCode == 6);
            CHECK(r.message.contains("STALE_POLICY_REVISION"));
            CHECK(!r.executed());
            CHECK(r.outcomeKnown());
            assertEnvelopeDiscipline(stats);
        }
        catch (const std::exception &e) {
            fprintf(stderr, "case threw (near line %d): %s
", __LINE__, e.what());
            fflush(stderr);
            failures++;
        }
        catch (...) {
            fprintf(stderr, "case threw (unknown, near line %d)
", __LINE__);
            fflush(stderr);
            failures++;
        }

        // ---- T4: typed payload error (code 0 + ErrorResp.error) ------------
        try {
            settle();
            auto stats = std::make_shared<Stats>();
            ServerThread svc(
                [](ServerConn &c, const libcore::RequestEnvelope &env) {
                    if (env.operation.value_or("") == "Hello") {
                        replyHello(c, env);
                        return;
                    }
                    libcore::ErrorResp e;
                    e.error = "ERR_CONFIG_POLICY: $.log.output is not permitted in service configs";
                    ServerThread::send(&c, okEnvelope(env, payloadOf(e)));
                },
                stats);
            libcore::LoadConfigReq req;
            req.core_config = std::string(R"({"log":{"output":"x"}})");
            const auto r = client.Start(req, 5000);
            CHECK(r.outcome == API::ServiceClient::Outcome::PayloadError);
            CHECK(r.executed()); // the service executed and answered a business error
            CHECK(r.outcomeKnown());
            CHECK(r.message.contains("ERR_CONFIG_POLICY"));
            assertEnvelopeDiscipline(stats);
        }
        catch (const std::exception &e) {
            fprintf(stderr, "case threw (near line %d): %s
", __LINE__, e.what());
            fflush(stderr);
            failures++;
        }
        catch (...) {
            fprintf(stderr, "case threw (unknown, near line %d)
", __LINE__);
            fflush(stderr);
            failures++;
        }

        // ---- T5: version mismatch on Hello, then recovery ------------------
        try {
            settle();
            auto stats = std::make_shared<Stats>();
            ServerThread svc(
                [&stats](ServerConn &c, const libcore::RequestEnvelope &env) {
                    if (env.operation.value_or("") != "Hello") {
                        replyHealth(c, env, true);
                        return;
                    }
                    const bool first = [&] {
                        std::lock_guard<std::mutex> lock(stats->mu);
                        return stats->connections == 1;
                    }();
                    if (first) {
                        // The real service refuses incompatible versions with
                        // a typed envelope error and disconnects.
                        ServerThread::send(&c, errorEnvelope(env, 1, "ERR_PROTOCOL_VERSION: client 1, service 2"));
                        QTimer::singleShot(30, c.sock, [sock = c.sock] { sock->close(); });
                        return;
                    }
                    replyHello(c, env);
                },
                stats);
            const auto r = client.Hello(nullptr, 5000);
            CHECK(r.outcome == API::ServiceClient::Outcome::EnvelopeError);
            CHECK(r.envelopeCode == 1);
            // The next call reconnects and handshakes cleanly.
            API::ServiceClient::HealthInfo health;
            const auto r2 = client.Health(&health, 5000);
            CHECK(r2.ok());
            CHECK(health.runtimeRunning);
            assertEnvelopeDiscipline(stats);
        }
        catch (const std::exception &e) {
            fprintf(stderr, "case threw (near line %d): %s
", __LINE__, e.what());
            fflush(stderr);
            failures++;
        }
        catch (...) {
            fprintf(stderr, "case threw (unknown, near line %d)
", __LINE__);
            fflush(stderr);
            failures++;
        }

        // ---- T6: response with an unexpected id -> protocol violation ------
        try {
            settle();
            auto stats = std::make_shared<Stats>();
            ServerThread svc(
                [](ServerConn &c, const libcore::RequestEnvelope &env) {
                    if (env.operation.value_or("") == "Hello") {
                        replyHello(c, env);
                        return;
                    }
                    if (env.operation.value_or("") == "Stop") {
                        // A response nobody waits for: the client must drop
                        // the connection instead of misattributing it.
                        libcore::RequestEnvelope forged = env;
                        forged.request_id = env.request_id.value_or(0) + 1000;
                        ServerThread::send(&c, okEnvelope(forged, {}));
                        return;
                    }
                    replyHealth(c, env, true);
                },
                stats);
            const auto r = client.Stop(5000);
            CHECK(r.outcome == API::ServiceClient::Outcome::ProtocolError);
            CHECK(r.sent);
            CHECK(!r.outcomeKnown()); // an unattributed response is an UNKNOWN outcome
            // Recovery: a fresh connection with a fresh Hello.
            API::ServiceClient::HealthInfo health;
            const auto r2 = client.Health(&health, 5000);
            CHECK(r2.ok());
            CHECK(r2.outcomeKnown());
            assertEnvelopeDiscipline(stats);
        }
        catch (const std::exception &e) {
            fprintf(stderr, "case threw (near line %d): %s
", __LINE__, e.what());
            fflush(stderr);
            failures++;
        }
        catch (...) {
            fprintf(stderr, "case threw (unknown, near line %d)
", __LINE__);
            fflush(stderr);
            failures++;
        }

        // ---- T7: connection break during Start -> UNKNOWN outcome ----------
        try {
            settle();
            auto stats = std::make_shared<Stats>();
            ServerThread svc(
                [](ServerConn &c, const libcore::RequestEnvelope &env) {
                    if (env.operation.value_or("") == "Hello") {
                        replyHello(c, env);
                        return;
                    }
                    if (env.operation.value_or("") == "Start") {
                        // Accept the Start and die before answering: the
                        // client must report an UNKNOWN outcome, never retry.
                        QTimer::singleShot(50, c.sock, [sock = c.sock] { sock->close(); });
                        return;
                    }
                    replyHealth(c, env, false);
                },
                stats);
            libcore::LoadConfigReq req;
            req.core_config = std::string(R"({"inbounds":[],"outbounds":[]})");
            const auto r = client.Start(req, 5000);
            CHECK(r.outcome == API::ServiceClient::Outcome::TransportError);
            CHECK(r.sent);
            CHECK(!r.outcomeKnown());
            // Recovery: new connection + fresh Hello; the state is then
            // established through Health.
            API::ServiceClient::HealthInfo health;
            const auto r2 = client.Health(&health, 5000);
            CHECK(r2.ok());
            CHECK(!health.runtimeRunning);
            CHECK(svc.connections() >= 2);
            assertEnvelopeDiscipline(stats);
        }
        catch (const std::exception &e) {
            fprintf(stderr, "case threw (near line %d): %s
", __LINE__, e.what());
            fflush(stderr);
            failures++;
        }
        catch (...) {
            fprintf(stderr, "case threw (unknown, near line %d)
", __LINE__);
            fflush(stderr);
            failures++;
        }

        // ---- T9: no answer within the deadline; late response dropped ------
        try {
            settle();
            auto stats = std::make_shared<Stats>();
            ServerThread svc(
                [](ServerConn &c, const libcore::RequestEnvelope &env) {
                    if (env.operation.value_or("") == "Hello") {
                        replyHello(c, env);
                        return;
                    }
                    if (env.operation.value_or("") == "Start") {
                        // Answer much later than the client budget.
                        QTimer::singleShot(2500, c.sock, [sock = c.sock, env] {
                            const QByteArray f = frameMessage(okEnvelope(env, payloadOf(libcore::ErrorResp{})));
                            if (sock->state() == QLocalSocket::ConnectedState) {
                                sock->write(f);
                                sock->flush();
                            }
                        });
                        return;
                    }
                    replyHealth(c, env, true);
                },
                stats);
            libcore::LoadConfigReq req;
            req.core_config = std::string(R"({"inbounds":[],"outbounds":[]})");
            const auto r = client.Start(req, 700);
            CHECK(r.outcome == API::ServiceClient::Outcome::Timeout);
            CHECK(r.sent);
            CHECK(!r.outcomeKnown());
            // State is established on a fresh connection; the late response
            // of the dropped one must never be misattributed.
            API::ServiceClient::HealthInfo health;
            const auto r2 = client.Health(&health, 5000);
            CHECK(r2.ok());
            CHECK(health.runtimeRunning);
            assertEnvelopeDiscipline(stats);
        }
        catch (const std::exception &e) {
            fprintf(stderr, "case threw (near line %d): %s
", __LINE__, e.what());
            fflush(stderr);
            failures++;
        }
        catch (...) {
            fprintf(stderr, "case threw (unknown, near line %d)
", __LINE__);
            fflush(stderr);
            failures++;
        }

        // ---- T12: oversized frame declaration -> violation, no crash -------
        try {
            settle();
            auto stats = std::make_shared<Stats>();
            ServerThread svc(
                [](ServerConn &c, const libcore::RequestEnvelope &env) {
                    if (env.operation.value_or("") == "Hello") {
                        replyHello(c, env);
                        return;
                    }
                    if (env.operation.value_or("") == "Health") {
                        replyHealth(c, env, true);
                        return;
                    }
                    QByteArray bogus;
                    bogus.resize(8);
                    qToLittleEndian<quint32>(0xFFFFFFFFu, bogus.data());
                    ServerThread::sendRaw(&c, bogus);
                    QTimer::singleShot(30, c.sock, [sock = c.sock] { sock->close(); });
                },
                stats);
            const auto r = client.Stop(5000);
            CHECK(r.outcome == API::ServiceClient::Outcome::ProtocolError);
            const auto r2 = client.Health(nullptr, 5000);
            CHECK(r2.ok());
            assertEnvelopeDiscipline(stats);
        }

#ifdef Q_OS_WIN
        // ---- T10: pipe access denial (SYSTEM-only DACL) ---------------------
        try {
            settle();
            PSECURITY_DESCRIPTOR sd = nullptr;
            ULONG sdSize = 0;
            CHECK(ConvertStringSecurityDescriptorToSecurityDescriptorW(
                L"D:P(A;;GA;;;SY)", SDDL_REVISION_1, &sd, &sdSize));
            SECURITY_ATTRIBUTES sa{};
            sa.nLength = sizeof(sa);
            sa.lpSecurityDescriptor = sd;
            sa.bInheritHandle = FALSE;
            HANDLE denied = CreateNamedPipeW(
                L"\\\\.\\pipe\\ProxyCoreService",
                PIPE_ACCESS_DUPLEX | FILE_FLAG_FIRST_PIPE_INSTANCE,
                PIPE_TYPE_BYTE | PIPE_READMODE_BYTE | PIPE_WAIT,
                1, 4096, 4096, 0, &sa);
            CHECK(denied != INVALID_HANDLE_VALUE);
            if (denied != INVALID_HANDLE_VALUE) {
                const auto r = client.Hello(nullptr, 5000);
                CHECK(r.outcome == API::ServiceClient::Outcome::AccessDenied);
                CHECK(!r.sent);
                CHECK(r.outcomeKnown());
                DisconnectNamedPipe(denied);
                CloseHandle(denied);
            }
            if (sd != nullptr) LocalFree(sd);
        }
#endif

        // ---- T8: the service pipe does not exist ----------------------------
        try {
            settle();
            const auto r = client.Hello(nullptr, 5000);
            CHECK(r.outcome == API::ServiceClient::Outcome::ConnectFailed);
            CHECK(!r.sent);
            CHECK(r.outcomeKnown());
            const auto r2 = client.Stop(5000);
            CHECK(r2.outcome == API::ServiceClient::Outcome::ConnectFailed);
            CHECK(r2.outcomeKnown());
        }
        catch (const std::exception &e) {
            fprintf(stderr, "case T8 threw: %s
", e.what());
            fflush(stderr);
            failures++;
        }
        catch (...) {
            fprintf(stderr, "case T8 threw (unknown)
");
            fflush(stderr);
            failures++;
        }
    }

} // namespace

int main(int argc, char *argv[]) {
    QCoreApplication app(argc, argv);

    std::atomic<bool> done{false};
    std::thread body([&done] {
        try {
            run_all();
        } catch (const std::exception &e) {
            fprintf(stderr, "run_all threw: %s
", e.what());
            fflush(stderr);
            failures++;
        } catch (...) {
            fprintf(stderr, "run_all threw (unknown)
");
            fflush(stderr);
            failures++;
        }
        done.store(true);
    });

    // Spin the event loop: the fake servers and the client io thread need it.
    QTimer poll;
    QObject::connect(&poll, &QTimer::timeout, [&] {
        if (done.load()) app.quit();
    });
    poll.start(20);
    app.exec();

    body.join();
    if (failures != 0) {
        qWarning("proxycore_service_client: %d check(s) failed", failures);
        return 1;
    }
    qInfo("proxycore_service_client: all checks passed");
    return 0;
}

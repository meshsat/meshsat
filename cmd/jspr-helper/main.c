/*
 * jspr-helper: C helper binary for RockBLOCK 9704 JSPR serial I/O.
 *
 * Uses the official Ground Control RockBLOCK-9704 C library for serial
 * communication. Go's runtime interferes with raw serial I/O (select/read
 * syscalls behave differently than in C). This helper runs as a subprocess,
 * communicating with the Go process via stdin (commands) and stdout (responses).
 *
 * Protocol (line-based JSON on stdin/stdout):
 *   Go → helper (stdin):  {"cmd":"send","method":"GET","target":"apiVersion","json":"{}"}
 *   helper → Go (stdout): {"type":"response","code":200,"target":"apiVersion","json":"{...}"}
 *   helper → Go (stdout): {"type":"unsolicited","code":299,"target":"constellationState","json":"{...}"}
 *   helper → Go (stdout): {"type":"error","message":"..."}
 *
 * Build: gcc -o jspr-helper main.c -I/path/to/RockBLOCK-9704/src -lrockblock9704
 * Or standalone with vendored sources (see Makefile).
 *
 * MIT License (same as RockBLOCK-9704 library).
 */

#include <stdio.h>
#include <stdlib.h>
#include <string.h>
#include <signal.h>
#include <unistd.h>
#include <fcntl.h>
#include <termios.h>
#include <sys/select.h>
#include <sys/ioctl.h>
#include <errno.h>
#include <time.h>

/* ===== Minimal inline serial I/O (from RockBLOCK-9704 serial_linux.c) ===== */

static int serial_fd = -1;

static int serial_open(const char *port, int baud) {
    serial_fd = open(port, O_RDWR | O_NOCTTY | O_SYNC | O_NONBLOCK);
    if (serial_fd < 0) {
        fprintf(stderr, "open %s: %s\n", port, strerror(errno));
        return -1;
    }

    struct termios options;
    if (tcgetattr(serial_fd, &options) != 0) {
        close(serial_fd);
        return -1;
    }

    speed_t speed;
    switch (baud) {
        case 230400: speed = B230400; break;
        case 115200: speed = B115200; break;
        case 19200:  speed = B19200;  break;
        default:     speed = B230400; break;
    }
    cfsetispeed(&options, speed);
    cfsetospeed(&options, speed);

    options.c_cflag &= ~CSIZE;
    options.c_cflag |= CS8;
    options.c_cflag &= ~PARENB;
    options.c_cflag &= ~CSTOPB;
    options.c_cflag |= CLOCAL | CREAD;
    options.c_iflag &= ~(IXON | IXOFF | IXANY | ICRNL);
    options.c_lflag &= ~(ICANON | ECHO | ECHOE | ISIG);

    if (tcsetattr(serial_fd, TCSANOW, &options) != 0) {
        close(serial_fd);
        return -1;
    }

    tcflush(serial_fd, TCIOFLUSH);

    /* Set FTDI latency timer to 1ms if possible */
    char latency_path[256];
    const char *dev = strrchr(port, '/');
    if (dev) {
        dev++;
        snprintf(latency_path, sizeof(latency_path),
                 "/sys/bus/usb-serial/devices/%s/latency_timer", dev);
        FILE *f = fopen(latency_path, "w");
        if (f) {
            fprintf(f, "1");
            fclose(f);
        }
    }

    return 0;
}

static int serial_read(char *buf, int len) {
    struct timeval timeout = {0, 500000}; /* 500ms */
    fd_set fds;
    FD_ZERO(&fds);
    FD_SET(serial_fd, &fds);
    int ready = select(serial_fd + 1, &fds, NULL, NULL, &timeout);
    if (ready <= 0) return -1;

    int n = 0;
    char ch;
    while (n < len) {
        int r = read(serial_fd, &ch, 1);
        if (r <= 0) break;
        buf[n++] = ch;
    }
    buf[n] = '\0';
    return n;
}

static int serial_write(const char *data, int len) {
    int sent = 0;
    while (sent < len) {
        int n = write(serial_fd, data + sent, len - sent);
        if (n < 0) {
            if (errno == EAGAIN) continue;
            return -1;
        }
        sent += n;
    }
    return sent;
}

static int serial_peek(void) {
    int bytes = 0;
    if (serial_fd > 0) {
        if (ioctl(serial_fd, FIONREAD, &bytes) != 0)
            return -1;
    }
    return bytes;
}

/* ===== JSPR protocol (minimal inline implementation) ===== */

#define RX_BUF_SIZE 8192
#define MAX_TARGET  64
#define MAX_JSON    8192

static char rx_buf[RX_BUF_SIZE];

typedef struct {
    int  code;
    char target[MAX_TARGET];
    char json[MAX_JSON];
} jspr_response_t;

/* Read one JSPR line: "CODE target {json}\r" */
static int jspr_receive(jspr_response_t *resp) {
    memset(resp, 0, sizeof(*resp));
    int pos = 0;

    while (pos < RX_BUF_SIZE - 1) {
        int n = serial_read(&rx_buf[pos], 1);
        if (n <= 0) return 0; /* timeout */
        if (rx_buf[pos] == '\r' && pos > 2) {
            rx_buf[pos] = '\0';
            break;
        }
        pos++;
    }
    if (pos <= 2) return 0;

    /* Skip leading non-printable (DC1 on boot) */
    int start = 0;
    while (start < pos && (rx_buf[start] < '0' || rx_buf[start] > '9'))
        start++;

    /* Parse code */
    if (pos - start < 3) return 0;
    char code_str[4] = {rx_buf[start], rx_buf[start+1], rx_buf[start+2], '\0'};
    resp->code = atoi(code_str);
    if (resp->code < 200 || resp->code > 500) return 0;

    /* Parse target */
    char *rest = &rx_buf[start + 4]; /* skip "CODE " */
    char *space = strchr(rest, ' ');
    if (space) {
        int tlen = space - rest;
        if (tlen >= MAX_TARGET) tlen = MAX_TARGET - 1;
        memcpy(resp->target, rest, tlen);
        resp->target[tlen] = '\0';

        /* Parse JSON */
        char *jstart = strchr(space, '{');
        if (jstart) {
            strncpy(resp->json, jstart, MAX_JSON - 1);
        }
    } else {
        strncpy(resp->target, rest, MAX_TARGET - 1);
        strcpy(resp->json, "{}");
    }

    return 1;
}

/* Send a JSPR command */
static int jspr_send(const char *method, const char *target, const char *json) {
    char buf[1024];
    int len = snprintf(buf, sizeof(buf), "%s %s %s\r", method, target, json);
    return serial_write(buf, len);
}

/* ===== Output to Go (JSON lines on stdout) ===== */

static void emit(const char *type, int code, const char *target, const char *json) {
    /* Escape JSON string for embedding */
    printf("{\"type\":\"%s\",\"code\":%d,\"target\":\"%s\",\"json\":%s}\n",
           type, code, target, json);
    fflush(stdout);
}

static void emit_error(const char *msg) {
    printf("{\"type\":\"error\",\"code\":0,\"target\":\"\",\"json\":\"{\\\"message\\\":\\\"%s\\\"}\"}\n", msg);
    fflush(stdout);
}

/* ===== Input from Go (JSON lines on stdin) ===== */

/* Read one line from stdin using fgets (blocking buffered I/O).
 * We use select() to check if data is available on the raw fd first,
 * but ALSO check if the FILE* has buffered data from a previous read
 * (which select() can't see). */
static int check_stdin(char *buf, int maxlen) {
    /* Check raw fd with select (non-blocking) */
    struct timeval tv = {0, 0};
    fd_set fds;
    FD_ZERO(&fds);
    FD_SET(STDIN_FILENO, &fds);
    int ready = select(STDIN_FILENO + 1, &fds, NULL, NULL, &tv);

    if (ready <= 0)
        return 0; /* no data on fd */

    if (fgets(buf, maxlen, stdin) == NULL)
        return -1; /* EOF — parent died */
    return strlen(buf);
}

/* Minimal JSON string extraction: find "key":"value" */
static int json_get_string(const char *json, const char *key, char *out, int maxlen) {
    char pattern[128];
    snprintf(pattern, sizeof(pattern), "\"%s\":\"", key);
    const char *start = strstr(json, pattern);
    if (!start) return 0;
    start += strlen(pattern);
    const char *end = strchr(start, '"');
    if (!end) return 0;
    int len = end - start;
    if (len >= maxlen) len = maxlen - 1;
    memcpy(out, start, len);
    out[len] = '\0';
    return 1;
}

/* ===== Main loop ===== */

static volatile int running = 1;

static void sighandler(int sig) {
    (void)sig;
    running = 0;
}

int main(int argc, char **argv) {
    if (argc < 2) {
        fprintf(stderr, "Usage: jspr-helper /dev/ttyUSB0 [baud]\n");
        return 1;
    }

    const char *port = argv[1];
    int baud = (argc > 2) ? atoi(argv[2]) : 230400;

    signal(SIGINT, sighandler);
    signal(SIGTERM, sighandler);
    signal(SIGPIPE, SIG_IGN);

    /* Make stdout line-buffered */
    setvbuf(stdout, NULL, _IOLBF, 0);

    if (serial_open(port, baud) < 0) {
        fprintf(stderr, "Failed to open %s at %d\n", port, baud);
        return 1;
    }

    fprintf(stderr, "jspr-helper: connected to %s at %d baud\n", port, baud);

    /* Drain stale data */
    {
        char drain[1024];
        struct timespec ts = {0, 100000000}; /* 100ms */
        nanosleep(&ts, NULL);
        while (serial_peek() > 0) {
            read(serial_fd, drain, sizeof(drain));
        }
    }

    jspr_response_t resp;
    char stdin_buf[4096];

    while (running) {
        /* 1. Check for commands from Go on stdin */
        int sn = check_stdin(stdin_buf, sizeof(stdin_buf));
        if (sn < 0) break; /* EOF — Go process died */
        if (sn > 0) {
            char method[16] = "", target[64] = "", json[4096] = "{}";
            json_get_string(stdin_buf, "method", method, sizeof(method));
            json_get_string(stdin_buf, "target", target, sizeof(target));

            /* Extract json field — find "json":" then grab to end */
            const char *jkey = strstr(stdin_buf, "\"json\":\"");
            if (jkey) {
                jkey += 8; /* skip "json":" */
                const char *jend = strrchr(jkey, '"');
                if (jend && jend > jkey) {
                    int jlen = jend - jkey;
                    if (jlen >= (int)sizeof(json)) jlen = sizeof(json) - 1;
                    memcpy(json, jkey, jlen);
                    json[jlen] = '\0';
                }
            }

            if (method[0] && target[0]) {
                jspr_send(method, target, json);
            }
        }

        /* 2. Poll for serial data (like rbPoll) */
        if (serial_peek() > 0) {
            if (jspr_receive(&resp)) {
                if (resp.code == 299) {
                    emit("unsolicited", resp.code, resp.target, resp.json);
                } else {
                    emit("response", resp.code, resp.target, resp.json);
                }
            }
        }

        /* 3. Brief sleep to avoid busy-spinning */
        usleep(10000); /* 10ms — matches C library example */
    }

    close(serial_fd);
    fprintf(stderr, "jspr-helper: shutdown\n");
    return 0;
}

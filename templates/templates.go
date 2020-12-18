package templates

var Get = map[string]string{
"controlpanel": `<!DOCTYPE html>
<html>
<head>
    <script src="https://vuejs.org/js/vue.min.js"></script>
    <style>
        label {
            font-weight: 600;
            margin-right: 10px;
        }
        table {
            border-collapse: collapse;
        }
        thead > tr > th {
            text-align: left;
            padding: 2px 10px;
        }
        tbody > tr:nth-child(even) {
            background-color: #ddd;
        }
        tbody > tr > td {
            padding: 2px 10px;
        }
        .error {
            color: #f00;
        }
        .controls {
            margin-bottom: 20px;
        }
        .page-control {
            display: inline-block;
            cursor: pointer;
            width: 14px;
            font-weight: 800;
            text-align: center;
        }
        .spaced-button {
            margin-left: 20px;
        }
        .no-uploads {
            background-color: #ffedcc;
        }

        /* Login Form */
        .login-form > * {
            display: block;
        }
        .login-form > input {
            margin-bottom: 10px;
        }
        .login-form > input:last-of-type {
            margin-bottom: 0;
        }
    </style>
</head>
<body>
    <template id="tmpl_manage">
        <div v-if="showAuth">
            <form @submit.prevent="login()" class="login-form">
                <label for="username">Username:</label>
                <input id="username" type="text" v-model="username">
                <label for="password">Password:</label>
                <input id="password" type="password" v-model="password">
                <input type="submit" value="Login">
            </form>
            <span v-if="error" class="error">
                {{error}}
            </span>
        </div>
        <div v-else>
            <div class="controls">
                <div>
                    <label for="filter">Filter:</label>
                    <input id="filter" type="text" v-model="filter">
                    <button @click="terminate()" class="spaced-button">Terminate Selected</button>
                    <button @click="logout()" class="spaced-button">Logout</button>
                </div>
                <span v-if="error" class="error">
                    {{error}}
                </span>
            </div>
            <table>
                <thead>
                    <tr>
                        <th><input type="checkbox" v-model="select_all"></td>
                        <th style="min-width: 250px;">id</th>
                        <th style="min-width: 120px;">remote</th>
                        <th style="min-width: 120px;">account</th>
                        <th style="min-width: 150px;">created</th>
                    </tr>
                </thead>
                <tbody>
                    <template v-if="uploads">
                        <tr v-for="upload in uploads">
                            <td><input type="checkbox" v-model="upload.terminate"></td>
                            <td>{{upload.id}}</td>
                            <td>{{upload.remote}}</td>
                            <td>{{upload.account || 'NONE'}}</td>
                            <td>{{new Date(upload.created * 1000).toLocaleString()}}</td>
                        </tr>
                    </template>
                    <template v-else>
                        <tr class="no-uploads">
                            <td>&nbsp;</td>
                            <td colspan="4">No Uploads</td>
                        </tr>
                    </template>
                </tbody>
                <tfoot>
                    <tr>
                        <td colspan="5">Page:
                            <span v-if="page > 1" @click="changePage(-1)" class="page-control">&lt;</span>
                            <span v-else class="page-control">&nbsp;</span>
                            {{page}} / {{pages}}
                            <span v-if="page < pages" @click="changePage(1)" class="page-control">&gt;</span>
                            <span v-else class="page-control">&nbsp;</span>
                        </td>
                    </tr>
                </tfoot>
            </table>
        </div>
    </template>
    <div id="app"></div>
    <script>
        new Vue({
            el: '#app',
            template: '#tmpl_manage',
            data() {
                return {
                    showAuth: false,
                    username: '',
                    password: '',
                    error: '',
                    uploads: [],
                    filter: '',
                    page: 1,
                    per_page: 50,
                    pages: 0,
                    select_all: false,
                    filterTimeout: 0,
                };
            },
            watch: {
                filter() {
                    if (this.filterTimeout) {
                        clearTimeout(this.filterTimeout);
                    }
                    this.filterTimeout = setTimeout(() => { this.getUploads(); }, 800);
                },
                select_all() {
                    for (const upload of this.uploads) {
                        upload.terminate = this.select_all;
                    }
                },
            },
            mounted() {
                this.getUploads();
            },
            methods: {
                changePage(direction) {
                    this.page += direction;
                    this.getUploads();
                },
                async login() {
                    const resp = await fetch('/admin/login', {
                        method: 'post',
                        cache: 'no-cache',
                        headers: {
                            'Content-Type': 'application/json'
                        },
                        body: JSON.stringify({ username: this.username, password: this.password }),
                    });

                    if (resp.ok) {
                        this.showAuth = false;
                        this.getUploads();
                    }
                },
                async logout() {
                    const resp = await fetch('/admin/logout', {
                        method: 'get',
                        cache: 'no-cache',
                        headers: {
                            'Content-Type': 'application/json'
                        },
                    });

                    if (resp.ok) {
                        this.showAuth = true;
                    }
                },
                async getUploads() {
                    let req = '';
                    req += '?filter=' + this.filter;
                    req += '&page=' + this.page;
                    req += '&per_page=' + this.per_page;

                    const resp = await fetch('/admin/get' + req, {
                        method: 'get',
                        cache: 'no-cache',
                        headers: {
                            'Content-Type': 'application/json'
                        },
                    });

                    if (resp.ok) {
                        const data = await resp.json();
                        this.select_all = false;
                        this.uploads = data.uploads;
                        this.pages = data.pages || 1;
                        if (this.page > this.pages) {
                            this.page = this.pages;
                        }
                    } else if (resp.status == 401) {
                        this.showAuth = true;
                    }
                },
                async terminate() {
                    const terminate = this.uploads.filter((u) => u.terminate).map((u) => u.id);
                    if (!terminate.length) {
                        // Nothing selected
                        return;
                    }
                    if (!window.confirm(`+"`"+`This will terminate `+"`"+` + terminate.length + ' selected uploads\n\nAre you sure?')) {
                        return;
                    }
                    const resp = await fetch('/admin/del', {
                        method: 'post',
                        cache: 'no-cache',
                        headers: {
                            'Content-Type': 'application/json'
                        },
                        body: JSON.stringify({terminate: terminate}),
                    });

                    if (resp.ok) {
                        this.getUploads();
                    }
                },
            },
        });
    </script>
</body>
</html>
`,
"webpreview": `<!DOCTYPE html>
<html>
    <head>
        <style>
            html, body {
                margin: 0;
                padding: 0;
            }
            .kiwi-embed-image {
                display: block;
            }
            /* style.extras */
        </style>
        <script>
            let last_width = 0;
            let last_height = 0;
            window.addEventListener('load', function () {
                postDimensions();
                const observerOptions = {
                    childList: true,
                    subtree: true,
                };
                const observer = new MutationObserver(mutationObserver);
                observer.observe(document.body, observerOptions);
            })

            function mutationObserver() {
                postDimensions();
            }

            function imgError() {
                window.parent.postMessage({ error: true }, '*');
            }

            function postDimensions() {
                if (!window.parent) {
                    // Nowhere to send messages
                    return;
                }
                const div = document.body.querySelector('#kiwi-embed-container');
                const width = div.scrollWidth;
                const height = div.scrollHeight;
                if (last_width === width && last_height === height) {
                    // No change
                    return;
                }
                last_width = width;
                last_height = height;
                window.parent.postMessage({ dimensions: { width, height } }, '*');
            }
        </script>
    </head>
    <body>
        <div id="kiwi-embed-container">
            {{body.html}}
        </div>
    </body>
</html>
`,
}

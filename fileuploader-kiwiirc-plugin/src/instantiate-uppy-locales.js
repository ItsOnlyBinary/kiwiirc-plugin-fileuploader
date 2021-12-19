import Translator from '@uppy/utils/lib/Translator';
import DefaultLang from '@uppy/locales/lib/en_US.js';

/* global _:true */

export default function instantiateUppyLocales(kiwiApi, uppy, scriptPath) {
    function loadLocale(_lang) {
        let lang = (_lang || kiwiApi.i18n.language).split('-');
        if (lang.length !== 2) {
            setDefaultLocale();
            return;
        }
        let langUppy = lang[0].toLowerCase() + '_' + lang[1].toUpperCase();

        let xhttp = new XMLHttpRequest();
        xhttp.onload = (event) => {
            if (xhttp.status !== 200) {
                // Failed to load the locale
                setDefaultLocale();
                return;
            }

            try {
                const locale = _.cloneDeep(DefaultLang);
                Object.assign(locale.strings, JSON.parse(xhttp.responseText));
                setLocale(locale);
            } catch {
                // failed to parse json
                setDefaultLocale();
            }
        };

        xhttp.open('GET', scriptPath + 'plugin-fileuploader/locales/uppy/' + langUppy + '.json');
        xhttp.send();
    }

    function setDefaultLocale() {
        setLocale(DefaultLang);
    }

    function setLocale(locale) {
        // update uppy core
        uppy.opts.locale = locale;
        uppy.translator = new Translator([DefaultLang, uppy.opts.locale]);
        uppy.locale = uppy.translator.locale;
        uppy.i18n = uppy.translator.translate.bind(uppy.translator);
        uppy.i18nArray = uppy.translator.translateArray.bind(uppy.translator);
        uppy.setState();

        // update uppy plugins
        uppy.iteratePlugins((plugin) => {
            if (plugin.translator) {
                plugin.translator = uppy.translator;
            }
            if (plugin.i18n) {
                plugin.i18n = uppy.i18n;
            }
            if (plugin.i18nArray) {
                plugin.i18nArray = uppy.i18nArray;
            }
            plugin.setPluginState();
        });
    }

    loadLocale();
    kiwiApi.state.$watch('user_settings.language', (lang) => {
        loadLocale(lang);
    });
};

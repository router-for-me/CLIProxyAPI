import DefaultTheme from 'vitepress/theme'
import { tabsClientScript } from '../plugins/content-tabs'

export default {
  extends: DefaultTheme,
  scripts: [
    {
      src: 'data:text/javascript,' + encodeURIComponent(tabsClientScript),
      type: 'text/javascript',
    },
  ],
}

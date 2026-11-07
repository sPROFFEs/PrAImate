package com.praimate.mobile;

import java.net.URI;
import java.util.Locale;

/** Routes native requests only to the server explicitly selected by the user. */
public final class Endpoint {
    public static final String APP_ORIGIN = "https://appassets.androidplatform.net";
    private Endpoint() {}
    public static boolean appOrigin(String value) {
        try { URI uri=new URI(value);return "https".equals(uri.getScheme())&&"appassets.androidplatform.net".equals(uri.getHost())&&uri.getPort()==-1&&uri.getRawUserInfo()==null; }
        catch(Exception error){return false;}
    }

    public static boolean sameHTTPSOrigin(String first,String second){
        try{
            URI a=new URI(first),b=new URI(second);
            if(!"https".equalsIgnoreCase(a.getScheme())||!"https".equalsIgnoreCase(b.getScheme())||a.getHost()==null||b.getHost()==null||a.getRawUserInfo()!=null||b.getRawUserInfo()!=null)return false;
            int ap=a.getPort()<0?443:a.getPort(),bp=b.getPort()<0?443:b.getPort();
            return a.getHost().equalsIgnoreCase(b.getHost())&&ap==bp;
        }catch(Exception invalid){return false;}
    }

    public static String normalize(String input, boolean emulator) {
        try {
            URI uri = new URI(input.trim());
            String scheme = uri.getScheme(), host = uri.getHost();
            if (host == null || scheme == null || uri.getRawUserInfo() != null || uri.getRawQuery() != null || uri.getRawFragment() != null
                    || !(uri.getRawPath().isEmpty() || uri.getRawPath().equals("/")) || uri.getPort() == 0 || uri.getPort() > 65535) {
                throw new IllegalArgumentException();
            }
            scheme = scheme.toLowerCase(Locale.ROOT); host = host.toLowerCase(Locale.ROOT);
            boolean loopback = host.equals("localhost") || host.equals("127.0.0.1") || host.equals("[::1]") || host.equals("::1") || (emulator && host.equals("10.0.2.2"));
            if (!scheme.equals("https") && !(scheme.equals("http") && loopback)) throw new IllegalArgumentException();
            return new URI(scheme, null, host, uri.getPort(), null, null, null).toASCIIString();
        } catch (Exception error) {
            throw new IllegalArgumentException("Enter an HTTPS server origin, for example https://praimate.example.com:8844. HTTP is limited to loopback.");
        }
    }

    public static String apiPath(String input) {
        try {
            URI uri = new URI(input);
            if (uri.isAbsolute() && (!"https".equals(uri.getScheme()) || !"appassets.androidplatform.net".equals(uri.getHost()) || uri.getPort() != -1)) throw new IllegalArgumentException();
            String path = uri.getRawPath();
            if (uri.getRawAuthority() != null && !uri.isAbsolute()) throw new IllegalArgumentException();
            if (uri.getRawUserInfo() != null || uri.getRawFragment() != null || path == null || !path.startsWith("/v1/") || !path.matches("/v1/[A-Za-z0-9_./-]+") || path.contains("..")) throw new IllegalArgumentException();
            return path + (uri.getRawQuery() == null ? "" : "?" + uri.getRawQuery());
        } catch (Exception error) { throw new IllegalArgumentException("Only PrAImate API requests are allowed"); }
    }

    public static String route(String input) {
        if (input == null || input.isEmpty()) return "";
        if (input.length() > 8192 || !input.startsWith("#") || input.contains("\n") || input.contains("\r")) throw new IllegalArgumentException("Invalid application route");
        return input;
    }
}
